package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const probe = `#!/usr/bin/python3
import json, os, signal, socket, subprocess, sys, time
data = json.load(open('/input'))
mode = data['mode']
if mode == 'isolation':
    assert not os.path.exists(data['host_file'])
    assert not os.path.exists('/etc/passwd')
    assert 'TUSKRAG_TEST_SECRET' not in os.environ
    for path in ['/input', '/usr/tuskrag-probe']:
        try:
            open(path, 'w')
        except OSError:
            pass
        else:
            sys.exit(31)
    if os.path.exists('/tuskrag-native'):
        try:
            open('/tuskrag-native', 'w')
        except OSError:
            pass
        else:
            sys.exit(31)
    sock = socket.socket()
    sock.settimeout(0.5)
    assert sock.connect_ex(('127.0.0.1', data['port'])) != 0
    sock.close()
    try:
        os.unshare(os.CLONE_NEWUSER)
    except OSError:
        pass
    else:
        sys.exit(33)
elif mode == 'valid':
    with open('/output/parsed.json', 'w') as result:
        json.dump({'schema_version': '1.0.0', 'pages': [], 'meta': {'parser_profile': 'test'}}, result)
    sys.exit(0)
elif mode == 'memory':
    try:
        _ = bytearray(1024 * 1024 * 1024)
    except MemoryError:
        sys.exit(12)
    sys.exit(32)
elif mode == 'diagnostic':
    print('x' * 100000)
elif mode == 'output_limit':
    signal.signal(signal.SIGXFSZ, signal.SIG_DFL)
    with open('/output/parsed.json', 'w') as result:
        result.write('x' * 100000)
elif mode == 'cpu':
    signal.signal(signal.SIGXCPU, signal.SIG_DFL)
    while True:
        pass
elif mode == 'cpu_tree':
    script = 'import time; start=time.process_time(); exec("while time.process_time()-start < 0.8:\\n pass")'
    children = [subprocess.Popen([sys.executable, '-c', script]) for _ in range(4)]
    for child in children:
        child.wait()
elif mode == 'memory_tree':
    script = 'import time; payload=bytearray(32*1024*1024); time.sleep(10)'
    children = [subprocess.Popen([sys.executable, '-c', script]) for _ in range(4)]
    for child in children:
        child.wait()
elif mode == 'pids_tree':
    children = []
    try:
        for _ in range(50):
            children.append(subprocess.Popen(['/usr/bin/sleep', '10']))
    except BlockingIOError:
        pass
    finally:
        for child in children:
            child.terminate()
        for child in children:
            child.wait()
elif mode == 'wait':
    subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)', data['marker']])
    with open('/output/parsed.json', 'w') as result:
        result.write('started')
        result.flush()
    time.sleep(60)
elif mode == 'exit':
    sys.exit(data['code'])
with open('/output/parsed.json', 'w') as result:
    json.dump({'ok': True}, result)
`

func sandboxFixture(t *testing.T) (*Sandbox, string) {
	t.Helper()
	bwrap := os.Getenv("TUSKRAG_TEST_BWRAP")
	if bwrap == "" {
		t.Skip("TUSKRAG_TEST_BWRAP is not set")
	}
	cgroupRoot := os.Getenv("TUSKRAG_TEST_CGROUP")
	if cgroupRoot == "" {
		t.Fatal("TUSKRAG_TEST_CGROUP is required for sandbox tests")
	}
	runtimeDir := t.TempDir()
	executable := filepath.Join(runtimeDir, "parser")
	if err := os.WriteFile(executable, []byte(probe), 0700); err != nil {
		t.Fatal(err)
	}
	tempDir := t.TempDir()
	sandbox, err := NewSandbox(SandboxConfig{
		Bubblewrap: bwrap, RuntimeDir: runtimeDir, Executable: executable, TempDir: tempDir, CgroupRoot: cgroupRoot,
		MemoryBytes: 256 * 1024 * 1024, CPUSeconds: 5, WallTime: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sandbox, tempDir
}

func sandboxInput(t *testing.T, value any) *os.File {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "document")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	return file
}

func TestSandboxIsolatesNetworkFilesAndEnvironment(t *testing.T) {
	sandbox, tempDir := sandboxFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("host listener unavailable: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	hostFile := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(hostFile, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUSKRAG_TEST_SECRET", "secret")
	for _, mime := range []string{"application/test", "text/plain"} {
		if mime == "text/plain" {
			config := sandbox.config
			config.NativeExecutable = config.Executable
			sandbox, err = NewSandbox(config)
			if err != nil {
				t.Fatal(err)
			}
		}
		input := sandboxInput(t, map[string]any{"mode": "isolation", "host_file": hostFile, "port": listener.Addr().(*net.TCPAddr).Port})
		output, err := sandbox.run(context.Background(), ParseInput{File: input, MIME: mime, Limits: json.RawMessage(`{}`)})
		if err != nil || string(output) != `{"ok": true}` {
			t.Fatalf("%s sandbox isolation failed: output=%q err=%v", mime, output, err)
		}
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary output was not removed: entries=%v err=%v", entries, err)
	}
}

func TestNativeSandboxRequiresExecutableAndLimitsProcessTree(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	file := sandboxInput(t, map[string]any{"mode": "memory_tree"})
	input := ParseInput{File: file, MIME: "text/plain", Limits: json.RawMessage(`{}`)}
	if _, err := sandbox.run(context.Background(), input); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("native parser ran without an executable: %v", err)
	}
	config := sandbox.config
	config.NativeExecutable = config.Executable
	config.MemoryBytes = 100 * 1024 * 1024
	sandbox, err := NewSandbox(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.run(context.Background(), input); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("native process tree escaped memory limit: %v", err)
	}
}

func TestNativeSandboxRejectsReplacedExecutable(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	config := sandbox.config
	config.NativeExecutable = config.Executable
	sandbox, err := NewSandbox(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(config.Executable, config.Executable+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Executable, []byte(probe), 0700); err != nil {
		t.Fatal(err)
	}
	file := sandboxInput(t, map[string]any{"mode": "valid"})
	if _, err := sandbox.run(context.Background(), ParseInput{File: file, MIME: "text/plain", Limits: json.RawMessage(`{}`)}); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("native executable replacement was accepted: %v", err)
	}
}

func TestParserCgroupCleanup(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	group, err := newParserCgroup(sandbox.config.CgroupRoot, sandbox.config.MemoryBytes, sandbox.config.CPUSeconds)
	if err != nil {
		t.Fatal(err)
	}
	path := group.path
	if err := group.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parser cgroup was not removed: %v", err)
	}
}

func TestSandboxRejectsNonCgroupDirectory(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	config := sandbox.config
	config.CgroupRoot = t.TempDir()
	if _, err := NewSandbox(config); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("non-cgroup directory accepted: %v", err)
	}
}

func TestSandboxLimitsAndExitCodes(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	for _, test := range []struct {
		mode string
		code int
		want error
	}{
		{"exit", 10, ErrUnsupportedFormat}, {"exit", 11, ErrEncrypted}, {"exit", 12, ErrLimitExceeded},
		{"exit", 13, ErrParse}, {"memory", 0, ErrLimitExceeded}, {"diagnostic", 0, ErrLimitExceeded}, {"output_limit", 0, ErrLimitExceeded},
	} {
		t.Run(fmt.Sprintf("%s-%d", test.mode, test.code), func(t *testing.T) {
			file := sandboxInput(t, map[string]any{"mode": test.mode, "code": test.code})
			_, err := sandbox.run(context.Background(), ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
			if !errors.Is(err, test.want) {
				t.Fatalf("wanted %v, got %v", test.want, err)
			}
		})
	}
}

func TestSandboxWallTimeLimit(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	sandbox.config.WallTime = 100 * time.Millisecond
	file := sandboxInput(t, map[string]any{"mode": "wait", "marker": "wall-time-test"})
	_, err := sandbox.run(context.Background(), ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("wanted timeout, got %v", err)
	}
}

func TestSandboxCPULimit(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	sandbox.config.CPUSeconds = 2
	file := sandboxInput(t, map[string]any{"mode": "cpu"})
	_, err := sandbox.run(context.Background(), ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("wanted CPU limit, got %v", err)
	}
}

func TestSandboxLimitsAcrossProcessTree(t *testing.T) {
	for _, mode := range []string{"cpu_tree", "memory_tree", "pids_tree"} {
		t.Run(mode, func(t *testing.T) {
			sandbox, _ := sandboxFixture(t)
			sandbox.config.CPUSeconds = 2
			switch mode {
			case "memory_tree":
				sandbox.config.MemoryBytes = 100 * 1024 * 1024
			case "pids_tree":
				sandbox.config.MemoryBytes = MaxMemoryBytes
			}
			file := sandboxInput(t, map[string]any{"mode": mode})
			_, err := sandbox.run(context.Background(), ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("process tree escaped %s limit: %v", mode, err)
			}
		})
	}
}

func TestParserRejectsOutputThatDoesNotMatchSchema(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	parser, err := NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	file := sandboxInput(t, map[string]any{"mode": "valid-transport"})
	_, err = parser.Parse(context.Background(), ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unvalidated output accepted: %v", err)
	}
}

func TestParserAcceptsValidatedOutput(t *testing.T) {
	sandbox, _ := sandboxFixture(t)
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	parser, err := NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	file := sandboxInput(t, map[string]any{"mode": "valid"})
	document, err := parser.Parse(context.Background(), ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
	if err != nil || document.Meta.ParserProfile != "test" {
		t.Fatalf("valid isolated output: document=%+v err=%v", document, err)
	}
}

func TestPythonParserWithinSandbox(t *testing.T) {
	bwrap := os.Getenv("TUSKRAG_TEST_BWRAP")
	runtimeDir := os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME")
	executable := os.Getenv("TUSKRAG_TEST_PARSER")
	cgroupRoot := os.Getenv("TUSKRAG_TEST_CGROUP")
	if bwrap == "" || runtimeDir == "" || executable == "" {
		t.Skip("isolated Python parser runtime is not configured")
	}
	if cgroupRoot == "" {
		t.Fatal("TUSKRAG_TEST_CGROUP is required for sandbox tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := ReadVersion(ctx, executable); err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandbox(SandboxConfig{
		Bubblewrap: bwrap, RuntimeDir: runtimeDir, Executable: executable, TempDir: t.TempDir(), CgroupRoot: cgroupRoot,
		MemoryBytes: 256 * 1024 * 1024, CPUSeconds: 5, WallTime: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	parser, err := NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	file := sandboxInput(t, map[string]any{"data": "unsupported test document"})
	_, err = parser.Parse(ctx, ParseInput{File: file, MIME: "application/unsupported", Limits: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("cross-language unsupported format: %v", err)
	}
	csvFile, err := os.Open("../../../../services/parser/tests/fixtures/csv/policy.csv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := csvFile.Close(); err != nil {
			t.Error(err)
		}
	})
	document, err := parser.Parse(ctx, ParseInput{File: csvFile, MIME: "text/csv", Limits: json.RawMessage(`{}`)})
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 1 ||
		len(document.Pages[0].Blocks[0].Cells) != 4 || len(document.Pages[0].Blocks[0].Cells[3]) != 3 || document.Pages[0].Blocks[0].Cells[3][1] != "450" {
		t.Fatalf("cross-language CSV parsing failed: document=%+v err=%v", document, err)
	}
	docxFile, err := os.Open("../../../../services/parser/tests/fixtures/docx/policy.docx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := docxFile.Close(); err != nil {
			t.Error(err)
		}
	})
	document, err = parser.Parse(ctx, ParseInput{File: docxFile, MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Limits: json.RawMessage(`{}`)})
	if err != nil || len(document.Pages) != 1 || len(document.Pages[0].Blocks) != 6 ||
		document.Pages[0].Blocks[0].Type != "heading" || document.Pages[0].Blocks[0].Text == nil || *document.Pages[0].Blocks[0].Text != "差旅管理办法" ||
		len(document.Pages[0].Blocks[4].Cells) != 3 || len(document.Pages[0].Blocks[4].Cells[1]) != 2 || document.Pages[0].Blocks[4].Cells[1][1] != "600元" {
		t.Fatalf("cross-language DOCX parsing failed: document=%+v err=%v", document, err)
	}
	encrypted, err := os.Open("../../../../services/parser/tests/fixtures/docx/encrypted.docx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := encrypted.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := parser.Parse(ctx, ParseInput{File: encrypted, MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Limits: json.RawMessage(`{}`)}); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("cross-language encrypted DOCX: %v", err)
	}
	xlsxFile, err := os.Open("../../../../services/parser/tests/fixtures/xlsx/travel.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := xlsxFile.Close(); err != nil {
			t.Error(err)
		}
	})
	xlsxMIME := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	document, err = parser.Parse(ctx, ParseInput{File: xlsxFile, MIME: xlsxMIME, Limits: json.RawMessage(`{}`)})
	if err != nil || len(document.Pages) != 2 || len(document.Pages[1].Blocks) != 2 ||
		len(document.Pages[1].Blocks[1].Cells) != 3 || len(document.Pages[1].Blocks[1].Cells[2]) != 3 ||
		document.Pages[1].Blocks[1].Cells[2][0] != "华东" || document.Pages[1].Blocks[1].Cells[2][2] != "60.0" {
		t.Fatalf("cross-language XLSX parsing failed: document=%+v err=%v", document, err)
	}
	formula, err := os.Open("../../../../services/parser/tests/fixtures/xlsx/formula.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := formula.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := parser.Parse(ctx, ParseInput{File: formula, MIME: xlsxMIME, Limits: json.RawMessage(`{}`)}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("cross-language XLSX formula rejection: %v", err)
	}
	pdfFile, err := os.Open("../../../../services/parser/tests/fixtures/pdf/policy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pdfFile.Close(); err != nil {
			t.Error(err)
		}
	})
	document, err = parser.Parse(ctx, ParseInput{File: pdfFile, MIME: "application/pdf", Limits: json.RawMessage(`{}`)})
	if err != nil || len(document.Pages) != 2 || len(document.Pages[0].Blocks) != 3 || len(document.Pages[1].Blocks) != 2 ||
		document.Pages[0].Blocks[0].Text == nil || *document.Pages[0].Blocks[0].Text != "差旅管理办法" ||
		len(document.Pages[0].Blocks[2].Cells) != 3 || document.Pages[0].Blocks[2].Cells[1][1] != "600" ||
		document.Pages[1].Blocks[0].Level == nil || *document.Pages[1].Blocks[0].Level != 2 {
		t.Fatalf("cross-language PDF parsing failed: document=%+v err=%v", document, err)
	}
	scanned, err := os.Open("../../../../services/parser/tests/fixtures/pdf/scanned.pdf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := scanned.Close(); err != nil {
			t.Error(err)
		}
	})
	document, err = parser.Parse(ctx, ParseInput{File: scanned, MIME: "application/pdf", Limits: json.RawMessage(`{}`)})
	if err != nil || len(document.Pages) != 1 || document.Pages[0].Signals.ImageCoverage == nil || *document.Pages[0].Signals.ImageCoverage != 1 || len(document.Pages[0].Blocks) != 0 {
		t.Fatalf("cross-language scanned PDF quality signal: document=%+v err=%v", document, err)
	}
	encryptedPDF, err := os.Open("../../../../services/parser/tests/fixtures/pdf/encrypted.pdf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := encryptedPDF.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := parser.Parse(ctx, ParseInput{File: encryptedPDF, MIME: "application/pdf", Limits: json.RawMessage(`{}`)}); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("cross-language encrypted PDF: %v", err)
	}
	staggeredPDF, err := os.Open("../../../../services/parser/tests/fixtures/pdf/columns-staggered.pdf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := staggeredPDF.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := parser.Parse(ctx, ParseInput{File: staggeredPDF, MIME: "application/pdf", Limits: json.RawMessage(`{}`)}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("cross-language staggered PDF columns: %v", err)
	}
}

func TestSandboxCancellationKillsDescendants(t *testing.T) {
	sandbox, tempDir := sandboxFixture(t)
	marker := "tuskrag-parser-child-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	file := sandboxInput(t, map[string]any{"mode": "wait", "marker": marker})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sandbox.run(ctx, ParseInput{File: file, MIME: "application/test", Limits: json.RawMessage(`{}`)})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("parser stopped before starting a child: %v", err)
		default:
		}
		entries, err := os.ReadDir(tempDir)
		if err != nil {
			t.Fatal(err)
		}
		started := false
		for _, entry := range entries {
			info, err := entry.Info()
			if err == nil && info.Size() > 0 {
				started = true
			}
		}
		if started {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("parser child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation result: %v", err)
	}
	for time.Now().Before(deadline) {
		found, err := processWithArgument(marker)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("parser descendant survived cancellation")
}

func processWithArgument(marker string) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && strings.Contains(string(data), marker) {
			return true, nil
		}
	}
	return false, nil
}

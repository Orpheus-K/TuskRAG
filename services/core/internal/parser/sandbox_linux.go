package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const diagnosticLimit = 16 * 1024

type Sandbox struct {
	config     SandboxConfig
	nativeInfo os.FileInfo
}

func NewSandbox(config SandboxConfig) (*Sandbox, error) {
	if !filepath.IsAbs(config.Bubblewrap) || !filepath.IsAbs(config.RuntimeDir) || !filepath.IsAbs(config.Executable) ||
		!filepath.IsAbs(config.TempDir) || !filepath.IsAbs(config.CgroupRoot) || config.MemoryBytes <= 0 || config.MemoryBytes > MaxMemoryBytes ||
		config.CPUSeconds <= 0 || config.CPUSeconds > MaxCPUSeconds || config.WallTime <= 0 || config.WallTime > MaxWallTime {
		return nil, errors.New("invalid parser sandbox configuration")
	}
	runtimeDir, err := filepath.EvalSymlinks(config.RuntimeDir)
	if err != nil {
		return nil, fmt.Errorf("resolve parser runtime: %w", err)
	}
	executable, err := filepath.EvalSymlinks(config.Executable)
	if err != nil {
		return nil, fmt.Errorf("resolve parser executable: %w", err)
	}
	if runtimeDir == "/" || executable == runtimeDir {
		return nil, errors.New("parser runtime must be a dedicated directory")
	}
	rel, err := filepath.Rel(runtimeDir, executable)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("parser executable must be inside the runtime directory")
	}
	info, err := os.Stat(runtimeDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("parser runtime directory is unavailable")
	}
	info, err = os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("parser executable is unavailable")
	}
	var nativeInfo os.FileInfo
	if config.NativeExecutable != "" {
		if !filepath.IsAbs(config.NativeExecutable) {
			return nil, errors.New("native parser executable must be absolute")
		}
		nativeExecutable, err := filepath.EvalSymlinks(config.NativeExecutable)
		if err != nil {
			return nil, fmt.Errorf("resolve native parser executable: %w", err)
		}
		info, err = os.Stat(nativeExecutable)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, errors.New("native parser executable is unavailable")
		}
		config.NativeExecutable = nativeExecutable
		nativeInfo = info
	}
	info, err = os.Stat(config.Bubblewrap)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, ErrSandboxUnavailable
	}
	config.RuntimeDir, config.Executable = runtimeDir, executable
	if err := checkCgroupRoot(config.CgroupRoot); err != nil {
		return nil, err
	}
	return &Sandbox{config: config, nativeInfo: nativeInfo}, nil
}

type boundedSink struct {
	remaining int
	exceeded  bool
}

func (b *boundedSink) Write(data []byte) (int, error) {
	if len(data) > b.remaining {
		b.exceeded = true
		return 0, ErrLimitExceeded
	}
	b.remaining -= len(data)
	return len(data), nil
}

func (s *Sandbox) run(ctx context.Context, input ParseInput) (_ []byte, resultErr error) {
	if input.File == nil || input.MIME == "" || len(input.MIME) > 255 || len(input.Limits) > 4096 || !json.Valid(input.Limits) {
		return nil, errors.New("invalid parser input")
	}
	native := input.MIME == "text/markdown" || input.MIME == "text/plain"
	if native && s.config.NativeExecutable == "" {
		return nil, ErrSandboxUnavailable
	}
	var nativeFile *os.File
	if native {
		var err error
		nativeFile, err = os.Open(s.config.NativeExecutable)
		if err != nil {
			return nil, fmt.Errorf("%w: open native parser executable: %w", ErrSandboxUnavailable, err)
		}
		defer func() { resultErr = errors.Join(resultErr, nativeFile.Close()) }()
		info, err := nativeFile.Stat()
		if err != nil || s.nativeInfo == nil || !os.SameFile(info, s.nativeInfo) {
			return nil, fmt.Errorf("%w: native parser executable changed", ErrSandboxUnavailable)
		}
	}
	info, err := input.File.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, errors.New("parser input must be a nonempty regular file")
	}
	if info.Size() > MaxInputBytes {
		return nil, ErrLimitExceeded
	}
	outputLimit := min(info.Size()*20, MaxOutputBytes)
	output, err := os.CreateTemp(s.config.TempDir, "tuskrag-parser-output-*")
	if err != nil {
		return nil, fmt.Errorf("create parser output: %w", err)
	}
	defer func() {
		if err := output.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close parser output: %w", err))
		}
		if err := os.Remove(output.Name()); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove parser output: %w", err))
		}
	}()
	group, err := newParserCgroup(s.config.CgroupRoot, s.config.MemoryBytes, s.config.CPUSeconds)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, group.close()) }()
	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-uts", "--unshare-ipc", "--disable-userns",
		"--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin", "--ro-bind", "/lib", "/lib", "--ro-bind-try", "/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "--size", "16777216", "--tmpfs", "/tmp",
	}
	if native {
		args = append(args, "--ro-bind-fd", "5", "/tuskrag-native")
	} else {
		args = append(args, "--ro-bind", s.config.RuntimeDir, s.config.RuntimeDir)
	}
	args = append(args,
		"--size", "4194304", "--tmpfs", "/output", "--ro-bind-fd", "3", "/input", "--bind-fd", "4", "/output/parsed.json",
		"--setenv", "PATH", "/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8", "--setenv", "HOME", "/nonexistent",
		"--setenv", "PYTHONNOUSERSITE", "1", "--setenv", "PYTHONDONTWRITEBYTECODE", "1", "--chdir", "/tmp",
		"--", "/usr/bin/prlimit")
	// Go reserves a large virtual address space; cgroup memory.max bounds its
	// resident memory and descendants without limiting virtual reservations.
	if !native {
		args = append(args, "--as="+strconv.FormatInt(s.config.MemoryBytes, 10))
	}
	args = append(args,
		"--cpu="+strconv.FormatInt(max(1, s.config.CPUSeconds-1), 10)+":"+strconv.FormatInt(s.config.CPUSeconds, 10),
		"--fsize="+strconv.FormatInt(outputLimit, 10), "--nofile=64", "--")
	if native {
		args = append(args, "/tuskrag-native", "native-parse", input.MIME, string(input.Limits))
	} else {
		args = append(args, s.config.Executable, "parse", "--input", "/input", "--mime", input.MIME, "--out", "/output", "--limits", string(input.Limits))
	}
	deadline, cancel := context.WithTimeout(ctx, s.config.WallTime)
	defer cancel()
	cmd := exec.CommandContext(deadline, s.config.Bubblewrap, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.ExtraFiles = []*os.File{input.File, output}
	if native {
		cmd.ExtraFiles = append(cmd.ExtraFiles, nativeFile)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, UseCgroupFD: true, CgroupFD: int(group.directory.Fd())}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := &boundedSink{remaining: diagnosticLimit}, &boundedSink{remaining: diagnosticLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	monitorResult := make(chan error, 1)
	go func() { monitorResult <- group.monitorCPU(monitorCtx, cancel) }()
	err = cmd.Run()
	stopMonitor()
	monitorErr := <-monitorResult
	if monitorErr != nil {
		return nil, monitorErr
	}
	exceeded, eventErr := group.exceeded()
	if eventErr != nil {
		return nil, eventErr
	}
	if exceeded {
		return nil, ErrLimitExceeded
	}
	if deadline.Err() != nil {
		if errors.Is(deadline.Err(), context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, deadline.Err()
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrLimitExceeded
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, fmt.Errorf("%w: launch isolated parser: %w", ErrSandboxUnavailable, err)
		}
		return nil, parserExitError(err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek parser output: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(output, outputLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read parser output: %w", err)
	}
	if int64(len(data)) > outputLimit {
		return nil, ErrLimitExceeded
	}
	if len(data) == 0 {
		return nil, ErrInvalidOutput
	}
	return data, nil
}

func parserExitError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 10:
			return ErrUnsupportedFormat
		case 11:
			return ErrEncrypted
		case 12:
			return ErrLimitExceeded
		case 13:
			return ErrParse
		case 128 + int(syscall.SIGXCPU), 128 + int(syscall.SIGXFSZ):
			return ErrLimitExceeded
		}
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && (status.Signal() == syscall.SIGXCPU || status.Signal() == syscall.SIGXFSZ) {
			return ErrLimitExceeded
		}
	}
	return fmt.Errorf("run isolated parser: %w", err)
}

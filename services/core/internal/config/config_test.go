package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadRejectsIncompleteConfiguration(t *testing.T) {
	for _, name := range []string{"TUSKRAG_ROLE", "TUSKRAG_DATABASE_URL", "TUSKRAG_LISTEN", "TUSKRAG_PARSER", "TUSKRAG_INSECURE_LOCAL_COOKIES", "TUSKRAG_MASTER_KEY_FILE", "TUSKRAG_STORAGE_DIR", "TUSKRAG_WORKER_TENANTS", "TUSKRAG_PARSER_RUNTIME_DIR", "TUSKRAG_BUBBLEWRAP", "TUSKRAG_CGROUP_ROOT", "TUSKRAG_PARSER_TEMP_DIR"} {
		t.Setenv(name, "")
	}
	if _, err := Load(); err == nil {
		t.Fatal("missing role accepted")
	}
	t.Setenv("TUSKRAG_ROLE", "api")
	t.Setenv("TUSKRAG_DATABASE_URL", "postgres://example")
	if _, err := Load(); err == nil {
		t.Fatal("missing listener accepted")
	}
	t.Setenv("TUSKRAG_LISTEN", "127.0.0.1:8080")
	if _, err := Load(); err != nil {
		t.Fatalf("valid API configuration: %v", err)
	}
	t.Setenv("TUSKRAG_INSECURE_LOCAL_COOKIES", "true")
	if cfg, err := Load(); err != nil || !cfg.InsecureLocalCookies {
		t.Fatalf("loopback development cookies rejected: config=%+v err=%v", cfg, err)
	}
	t.Setenv("TUSKRAG_LISTEN", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("insecure cookies accepted on a public listener")
	}
	t.Setenv("TUSKRAG_INSECURE_LOCAL_COOKIES", "")
	t.Setenv("TUSKRAG_LISTEN", "127.0.0.1:8080")
	t.Setenv("TUSKRAG_MASTER_KEY_FILE", "relative.key")
	if _, err := Load(); err == nil {
		t.Fatal("relative master key path accepted")
	}
	t.Setenv("TUSKRAG_MASTER_KEY_FILE", "")
	t.Setenv("TUSKRAG_STORAGE_DIR", "relative")
	if _, err := Load(); err == nil {
		t.Fatal("relative storage directory accepted")
	}
	t.Setenv("TUSKRAG_STORAGE_DIR", "")
	t.Setenv("TUSKRAG_LISTEN", "127.0.0.1:invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid TCP port accepted")
	}
	t.Setenv("TUSKRAG_ROLE", "worker")
	if _, err := Load(); err == nil {
		t.Fatal("missing worker parser accepted")
	}
}

func TestLoadWorkerRequiresIsolatedParserAndTenants(t *testing.T) {
	t.Setenv("TUSKRAG_ROLE", "worker")
	t.Setenv("TUSKRAG_DATABASE_URL", "postgres://example")
	t.Setenv("TUSKRAG_MASTER_KEY_FILE", "")
	root := t.TempDir()
	for _, item := range []struct{ name, value string }{
		{"TUSKRAG_STORAGE_DIR", filepath.Join(root, "storage")}, {"TUSKRAG_PARSER", filepath.Join(root, "runtime", "parse")},
		{"TUSKRAG_PARSER_RUNTIME_DIR", filepath.Join(root, "runtime")}, {"TUSKRAG_BUBBLEWRAP", filepath.Join(root, "bwrap")},
		{"TUSKRAG_CGROUP_ROOT", filepath.Join(root, "cgroup")}, {"TUSKRAG_PARSER_TEMP_DIR", filepath.Join(root, "temp")},
	} {
		t.Setenv(item.name, item.value)
	}
	t.Setenv("TUSKRAG_WORKER_TENANTS", "1, 9,42")
	config, err := Load()
	if err != nil || !reflect.DeepEqual(config.WorkerTenants, []int64{1, 9, 42}) {
		t.Fatalf("valid worker configuration: %+v err=%v", config.WorkerTenants, err)
	}
	for _, value := range []string{"", "1,,2", "0", "-1", "1,1", "x", strings.Repeat("1,", 64) + "2"} {
		t.Setenv("TUSKRAG_WORKER_TENANTS", value)
		if _, err := Load(); err == nil {
			t.Fatalf("invalid worker tenants accepted: %q", value)
		}
	}
	t.Setenv("TUSKRAG_WORKER_TENANTS", "1")
	t.Setenv("TUSKRAG_BUBBLEWRAP", "relative")
	if _, err := Load(); err == nil {
		t.Fatal("relative sandbox executable accepted")
	}
}

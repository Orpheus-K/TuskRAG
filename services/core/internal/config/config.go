package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Role                 string
	DatabaseURL          string
	Listen               string
	Parser               string
	ParserRuntimeDir     string
	Bubblewrap           string
	CgroupRoot           string
	ParserTempDir        string
	WorkerTenants        []int64
	InsecureLocalCookies bool
	MasterKeyFile        string
	StorageDirectory     string
}

func Load() (Config, error) {
	c := Config{
		Role:             os.Getenv("TUSKRAG_ROLE"),
		DatabaseURL:      os.Getenv("TUSKRAG_DATABASE_URL"),
		Listen:           os.Getenv("TUSKRAG_LISTEN"),
		Parser:           os.Getenv("TUSKRAG_PARSER"),
		ParserRuntimeDir: os.Getenv("TUSKRAG_PARSER_RUNTIME_DIR"),
		Bubblewrap:       os.Getenv("TUSKRAG_BUBBLEWRAP"),
		CgroupRoot:       os.Getenv("TUSKRAG_CGROUP_ROOT"),
		ParserTempDir:    os.Getenv("TUSKRAG_PARSER_TEMP_DIR"),
		MasterKeyFile:    os.Getenv("TUSKRAG_MASTER_KEY_FILE"),
		StorageDirectory: os.Getenv("TUSKRAG_STORAGE_DIR"),
	}
	if c.Role == "" {
		return Config{}, errors.New("TUSKRAG_ROLE is required")
	}
	switch c.Role {
	case "api", "worker", "all", "migrate":
	default:
		return Config{}, fmt.Errorf("unsupported TUSKRAG_ROLE %q", c.Role)
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return Config{}, errors.New("TUSKRAG_DATABASE_URL is required")
	}
	if c.MasterKeyFile != "" && !filepath.IsAbs(c.MasterKeyFile) {
		return Config{}, errors.New("TUSKRAG_MASTER_KEY_FILE must be an absolute path")
	}
	if c.StorageDirectory != "" && !filepath.IsAbs(c.StorageDirectory) {
		return Config{}, errors.New("TUSKRAG_STORAGE_DIR must be an absolute path")
	}
	if c.Role == "api" || c.Role == "all" {
		host, port, err := net.SplitHostPort(c.Listen)
		if err != nil {
			return Config{}, fmt.Errorf("invalid TUSKRAG_LISTEN: %w", err)
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return Config{}, errors.New("TUSKRAG_LISTEN must contain a valid TCP port")
		}
		if raw := os.Getenv("TUSKRAG_INSECURE_LOCAL_COOKIES"); raw != "" {
			c.InsecureLocalCookies, err = strconv.ParseBool(raw)
			if err != nil {
				return Config{}, errors.New("TUSKRAG_INSECURE_LOCAL_COOKIES must be a boolean")
			}
			ip := net.ParseIP(host)
			if c.InsecureLocalCookies && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return Config{}, errors.New("insecure cookies require a loopback listener")
			}
		}
	}
	if c.Role == "worker" || c.Role == "all" {
		paths := []struct{ name, value string }{
			{"TUSKRAG_STORAGE_DIR", c.StorageDirectory}, {"TUSKRAG_PARSER", c.Parser},
			{"TUSKRAG_PARSER_RUNTIME_DIR", c.ParserRuntimeDir}, {"TUSKRAG_BUBBLEWRAP", c.Bubblewrap},
			{"TUSKRAG_CGROUP_ROOT", c.CgroupRoot}, {"TUSKRAG_PARSER_TEMP_DIR", c.ParserTempDir},
		}
		for _, path := range paths {
			if !filepath.IsAbs(path.value) {
				return Config{}, fmt.Errorf("%s must be an absolute path for worker roles", path.name)
			}
		}
		var err error
		c.WorkerTenants, err = parseWorkerTenants(os.Getenv("TUSKRAG_WORKER_TENANTS"))
		if err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

func parseWorkerTenants(raw string) ([]int64, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || len(parts) > 64 {
		return nil, errors.New("TUSKRAG_WORKER_TENANTS must contain 1 to 64 tenant IDs")
	}
	tenants := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("TUSKRAG_WORKER_TENANTS must contain positive integer IDs")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("TUSKRAG_WORKER_TENANTS must not contain duplicate IDs")
		}
		seen[id] = struct{}{}
		tenants = append(tenants, id)
	}
	return tenants, nil
}

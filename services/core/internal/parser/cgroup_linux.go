package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cgroup2Magic = 0x63677270

type parserCgroup struct {
	path       string
	directory  *os.File
	cpuSeconds int64
}

func checkCgroupRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: cgroup root unavailable", ErrSandboxUnavailable)
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(root, &filesystem); err != nil || filesystem.Type != cgroup2Magic {
		return fmt.Errorf("%w: cgroup v2 root required", ErrSandboxUnavailable)
	}
	return nil
}

func newParserCgroup(root string, memoryBytes, cpuSeconds int64) (_ *parserCgroup, resultErr error) {
	path, err := os.MkdirTemp(root, "tuskrag-parser-")
	if err != nil {
		return nil, fmt.Errorf("%w: create parser cgroup: %w", ErrSandboxUnavailable, err)
	}
	defer func() {
		if resultErr != nil {
			if err := os.Remove(path); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove parser cgroup: %w", err))
			}
		}
	}()
	settings := []struct{ name, value string }{
		{"memory.max", strconv.FormatInt(memoryBytes, 10)}, {"memory.swap.max", "0"},
		{"memory.oom.group", "1"}, {"pids.max", "32"},
	}
	for _, setting := range settings {
		if err := os.WriteFile(filepath.Join(path, setting.name), []byte(setting.value), 0600); err != nil {
			return nil, fmt.Errorf("%w: configure parser cgroup: %w", ErrSandboxUnavailable, err)
		}
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open parser cgroup: %w", ErrSandboxUnavailable, err)
	}
	return &parserCgroup{path: path, directory: directory, cpuSeconds: cpuSeconds}, nil
}

func (g *parserCgroup) monitorCPU(ctx context.Context, cancel context.CancelFunc) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			usage, err := g.counter("cpu.stat", "usage_usec")
			if err != nil {
				cancel()
				return err
			}
			if usage >= g.cpuSeconds*1_000_000 {
				cancel()
				return ErrLimitExceeded
			}
		}
	}
}

func (g *parserCgroup) exceeded() (bool, error) {
	for _, item := range []struct {
		file  string
		key   string
		limit int64
	}{
		{"cpu.stat", "usage_usec", g.cpuSeconds * 1_000_000},
		{"memory.events", "oom", 1},
		{"memory.events", "oom_kill", 1},
		{"pids.events", "max", 1},
	} {
		value, err := g.counter(item.file, item.key)
		if err != nil {
			return false, err
		}
		if value >= item.limit {
			return true, nil
		}
	}
	return false, nil
}

func (g *parserCgroup) counter(file, key string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(g.path, file))
	if err != nil {
		return 0, fmt.Errorf("read parser cgroup %s: %w", file, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == key {
			value, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || value < 0 {
				return 0, fmt.Errorf("invalid parser cgroup counter %s", key)
			}
			return value, nil
		}
	}
	return 0, fmt.Errorf("missing parser cgroup counter %s", key)
}

func (g *parserCgroup) close() error {
	var result error
	if err := os.WriteFile(filepath.Join(g.path, "cgroup.kill"), []byte("1"), 0600); err != nil {
		result = errors.Join(result, fmt.Errorf("kill parser cgroup: %w", err))
	}
	if err := g.directory.Close(); err != nil {
		result = errors.Join(result, fmt.Errorf("close parser cgroup: %w", err))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Remove(g.path)
		if err == nil {
			return result
		}
		if !errors.Is(err, syscall.EBUSY) || time.Now().After(deadline) {
			return errors.Join(result, fmt.Errorf("remove parser cgroup: %w", err))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

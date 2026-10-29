package secrets

import (
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"runtime"
)

// ReadPrivateFile reads a bounded regular file. Windows ACLs must be restricted by the operator.
func ReadPrivateFile(path string, maxBytes int64) (result []byte, resultErr error) {
	if !filepath.IsAbs(path) || maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return nil, errors.New("absolute path and positive file-size limit are required")
	}
	file, err := openPrivateFile(path)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close private file: %w", err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect private file: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("file must be regular and private to its owner")
	}
	result, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		clear(result)
		return nil, fmt.Errorf("read private file: %w", err)
	}
	if int64(len(result)) > maxBytes {
		clear(result)
		return nil, errors.New("private file exceeds size limit")
	}
	return result, nil
}

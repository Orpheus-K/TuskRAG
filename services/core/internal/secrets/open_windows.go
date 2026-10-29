package secrets

import (
	"errors"
	"os"
	"strings"
)

func openPrivateFile(path string) (*os.File, error) {
	if strings.HasPrefix(strings.ToLower(path), `\\.\pipe\`) {
		return nil, errors.New("named pipes are not private files")
	}
	return os.Open(path)
}

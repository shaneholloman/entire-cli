// Package osroot provides traversal-resistant file I/O helpers built on os.Root
// (Go 1.24+). These helpers ensure that file operations cannot escape a scoped
// directory, preventing symlink attacks and TOCTOU races at the kernel level.
//
// os.Root supports: Open, OpenFile, Create, Stat, Lstat, Mkdir, Remove, OpenRoot.
// os.Root does NOT support: MkdirAll, WriteFile, ReadFile, Rename, RemoveAll.
// For unsupported operations, callers should use standard os functions with
// lexical validation.
//
// Errors from these functions are returned unwrapped so that callers can use
// os.IsNotExist() and errors.Is() directly without losing the original sentinel.
package osroot

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ReadFile reads the named file relative to root using os.Root for
// traversal-resistant access. The kernel enforces that the read cannot
// escape the root directory, preventing symlink and TOCTOU attacks.
func ReadFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err //nolint:wrapcheck // preserve original error for os.IsNotExist checks
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err //nolint:wrapcheck // preserve original error
	}
	return data, nil
}

// WriteFile writes data to the named file relative to root using os.Root
// for traversal-resistant access. Creates the file if it doesn't exist,
// truncates it if it does. The kernel enforces that the write cannot escape
// the root directory.
func WriteFile(root *os.Root, name string, data []byte, perm os.FileMode) (retErr error) {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err //nolint:wrapcheck // preserve original error for os.IsNotExist checks
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	if _, err := f.Write(data); err != nil {
		return err //nolint:wrapcheck // preserve original error
	}
	return nil
}

// MkdirAll creates the directory named by name, along with any necessary
// parents, relative to root. Each level is created with os.Root.Mkdir so the
// kernel enforces containment: unlike os.MkdirAll, it cannot create directories
// outside root. A name that escapes root (absolute, or containing ".." segments
// that climb above root) is rejected by os.Root and returns an error. Already-
// existing directories are tolerated. name may use either OS-native or forward
// slashes; an empty or "." name is a no-op.
func MkdirAll(root *os.Root, name string, perm os.FileMode) error {
	name = strings.Trim(filepath.ToSlash(name), "/")
	if name == "" || name == "." {
		return nil
	}

	cur := ""
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		if err := root.Mkdir(cur, perm); err != nil && !errors.Is(err, fs.ErrExist) {
			return err //nolint:wrapcheck // preserve original error (incl. traversal rejection) for callers
		}
	}
	return nil
}

// Remove removes the named file relative to root using os.Root for
// traversal-resistant access. Returns nil if the file doesn't exist.
func Remove(root *os.Root, name string) error {
	err := root.Remove(name)
	if err != nil && !os.IsNotExist(err) {
		return err //nolint:wrapcheck // preserve original error
	}
	return nil
}

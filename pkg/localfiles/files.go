// Package localfiles provides regular-file access within an opened directory.
package localfiles

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// OpenRegular opens a file within root, rejecting nonregular files before
// truncating them. Relative symlinks must remain within root.
func OpenRegular(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	if flag&(os.O_CREATE|os.O_EXCL) != os.O_CREATE|os.O_EXCL {
		if info, err := root.Stat(name); err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("file %s must be a regular file", name)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	file, err := root.OpenFile(name, flag&^os.O_TRUNC|nonblockFlag, perm)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("file %s must be a regular file", name)
	}
	if flag&os.O_TRUNC != 0 {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

// ReadFile reads a regular file within root.
func ReadFile(root *os.Root, name string) ([]byte, error) {
	file, err := OpenRegular(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

// ReadDir reads a directory within root without blocking on a substituted FIFO.
func ReadDir(root *os.Root, name string) ([]os.DirEntry, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("file %s must be a directory", name)
	}
	file, err := root.OpenFile(name, os.O_RDONLY|nonblockFlag, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return file.ReadDir(-1)
}

// CreateTemp creates an exclusive regular file directly within root with mode
// 0600. On Windows, the file inherits its directory's access control list.
// The returned name is relative to root. The last '*' in pattern is replaced
// with random text, or random text is appended when pattern has no '*'.
func CreateTemp(root *os.Root, pattern string) (*os.File, string, error) {
	if strings.ContainsAny(pattern, `/\`) {
		return nil, "", fmt.Errorf("temporary file pattern must not contain path separators")
	}
	prefix, suffix := pattern, ""
	if index := strings.LastIndexByte(pattern, '*'); index >= 0 {
		prefix, suffix = pattern[:index], pattern[index+1:]
	}
	for range 10 {
		name := prefix + rand.Text() + suffix
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("create temporary file: %w", os.ErrExist)
}

// WriteFile writes a regular file within root, creating it exclusively unless
// overwrite is true. Overwrites use ReplaceFile except on Windows, where the
// existing file is truncated after verification to preserve its access control
// list. Windows writes retain the file's identity and are visible through any
// hard links. New files use mode 0600, or inherited access controls on Windows.
func WriteFile(root *os.Root, name string, data []byte, overwrite bool) error {
	if overwrite && runtime.GOOS != "windows" {
		return ReplaceFile(root, name, data)
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := OpenRegular(root, name, flag, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// ReplaceFile publishes a new regular file within root using a staged rename.
// The replacement uses mode 0600, or inherited access controls on Windows;
// existing file permissions are not preserved. Rename is atomic on Unix.
func ReplaceFile(root *os.Root, name string, data []byte) error {
	if info, err := root.Stat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file %s must be a regular file", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, tempName, err := CreateTemp(root, ".profile-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tempName) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(tempName, name)
}

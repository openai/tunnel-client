package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/openai/tunnel-client/pkg/codexplugin/state"
	"github.com/openai/tunnel-client/pkg/localfiles"
)

const maxManagedHealthURLBytes = 8192

func openManagedDirectory(root state.Root, directory string, create bool) (*os.Root, error) {
	if directory != "logs" && directory != "health" {
		return nil, fmt.Errorf("unsupported managed directory %q", directory)
	}
	if strings.TrimSpace(root.Path) == "" {
		return nil, fmt.Errorf("state root is required")
	}
	if create {
		if err := os.MkdirAll(root.Path, 0o755); err != nil {
			return nil, err
		}
	}
	stateDir, err := os.OpenRoot(root.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stateDir.Close() }()
	if create {
		if err := stateDir.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
	}
	// The terminal dot requires directory traversal, so a substituted FIFO
	// cannot block the subroot open.
	dir, err := stateDir.OpenRoot(directory + string(filepath.Separator) + ".")
	if err != nil {
		return nil, err
	}
	openedInfo, err := dir.Stat(".")
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	info, err := stateDir.Lstat(directory)
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	if !info.IsDir() || !os.SameFile(info, openedInfo) {
		_ = dir.Close()
		return nil, fmt.Errorf("managed %s directory must not be a symlink or change while opening", directory)
	}
	return dir, nil
}

func managedFileName(dir *os.Root, path string) (string, error) {
	name := filepath.Base(path)
	if strings.TrimSpace(path) == "" || name == "." || name == ".." {
		return "", fmt.Errorf("managed file path is required")
	}
	openedInfo, err := dir.Stat(".")
	if err != nil {
		return "", err
	}
	// Persisted paths may use a different symlink spelling of the selected
	// root. Compare parent identity; all file operations still use dir.
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	if !os.SameFile(parentInfo, openedInfo) {
		return "", fmt.Errorf("managed file %s must be directly within %s", path, dir.Name())
	}
	return name, nil
}

// OpenManagedFile opens a regular file beneath the selected managed directory.
// Writes keep private permissions on the same file handle used by the caller.
func OpenManagedFile(root state.Root, directory, path string, flags int) (*os.File, error) {
	dir, err := openManagedDirectory(root, directory, flags&os.O_CREATE != 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	name, err := managedFileName(dir, path)
	if err != nil {
		return nil, err
	}
	if directory == "logs" {
		if info, err := dir.Lstat(name); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("log file %s must not be a symlink", path)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	file, err := localfiles.OpenRegular(dir, name, flags&^os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if directory == "logs" {
		info, err := dir.Lstat(name)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		openedInfo, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, openedInfo) {
			_ = file.Close()
			return nil, fmt.Errorf("log file %s must not be a symlink or change while opening", path)
		}
	}
	if flags&(os.O_WRONLY|os.O_RDWR) != 0 {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	if flags&os.O_TRUNC != 0 {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

// ReadManagedHealthURL reads a bounded regular health file from the state root.
// Missing or invalid files retain the not-yet-ready result used during startup.
func ReadManagedHealthURL(root state.Root, path string) string {
	file, err := OpenManagedFile(root, "health", path, os.O_RDONLY)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxManagedHealthURLBytes+1))
	if err != nil || len(data) > maxManagedHealthURLBytes {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ManagedFileInfo inspects a regular file through the same confined open as reads.
func ManagedFileInfo(root state.Root, directory, path string) (os.FileInfo, error) {
	file, err := OpenManagedFile(root, directory, path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return file.Stat()
}

// ManagedFileNames lists entries beneath an opened managed directory.
func ManagedFileNames(root state.Root, directory string) ([]string, error) {
	dir, err := openManagedDirectory(root, directory, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := localfiles.ReadDir(dir, ".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// RemoveManagedFile removes a directory entry without following its leaf symlink.
func RemoveManagedFile(root state.Root, directory, path string) error {
	dir, err := openManagedDirectory(root, directory, false)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name, err := managedFileName(dir, path)
	if err != nil {
		return err
	}
	return dir.Remove(name)
}

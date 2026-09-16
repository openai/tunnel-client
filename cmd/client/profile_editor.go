package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Only non-evaluating options belong here. Editors, their startup configuration,
// and executable lookup remain trusted local software.
var profileEditorFlags = map[string][]string{
	"vi":      {"-R", "-n"},
	"vim":     {"-f", "--nofork", "-n", "-R"},
	"nvim":    {"-f", "--nofork", "-n", "-R"},
	"nano":    {"-w", "--nowrap", "-l", "--linenumbers", "-m", "--mouse", "-E", "--tabstospaces"},
	"emacs":   {"-nw", "--no-window-system", "-Q", "--quick", "-q", "--no-init-file", "--no-site-file"},
	"code":    {"-w", "--wait", "-n", "--new-window", "-r", "--reuse-window", "--disable-extensions"},
	"subl":    {"-w", "--wait", "-n", "--new-window", "-b", "--background"},
	"notepad": {},
}

func profileEditorCommand(editor, path string) (*exec.Cmd, error) {
	parts, err := splitProfileEditor(editor)
	if err != nil {
		return nil, fmt.Errorf("invalid VISUAL or EDITOR: %w", err)
	}
	name, err := installedProfileEditorName(parts[0])
	if err != nil {
		return nil, err
	}
	allowed := profileEditorFlags[name]
	for _, arg := range parts[1:] {
		if !slices.Contains(allowed, arg) {
			return nil, fmt.Errorf("unsupported option %q for profile editor %q; shell commands and editor evaluation options are not allowed", arg, name)
		}
	}
	executable, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("find installed profile editor %q: %w", name, err)
	}
	if parts[0] != filepath.Base(parts[0]) {
		installed, err := os.Stat(executable)
		if err != nil {
			return nil, fmt.Errorf("inspect installed profile editor %q: %w", name, err)
		}
		selected, err := os.Stat(parts[0])
		if err != nil {
			return nil, fmt.Errorf("inspect selected profile editor: %w", err)
		}
		if !os.SameFile(installed, selected) {
			return nil, fmt.Errorf("profile editor path must select the installed %q executable found in PATH", name)
		}
	}
	// An absolute filename cannot be interpreted as an option or a +command.
	profilePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve profile path: %w", err)
	}
	return exec.Command(executable, append(parts[1:], profilePath)...), nil
}

func installedProfileEditorName(selected string) (string, error) {
	name := strings.ToLower(filepath.Base(selected))
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".exe")
	}
	if _, ok := profileEditorFlags[name]; ok {
		return name, nil
	}
	if name != "editor" {
		return "", fmt.Errorf("unsupported profile editor %q; use vi, vim, nvim, nano, emacs, code, subl, or notepad", name)
	}
	alias, err := exec.LookPath(selected)
	if err != nil {
		return "", fmt.Errorf("find installed editor alias: %w", err)
	}
	selectedFile, err := os.Stat(alias)
	if err != nil {
		return "", fmt.Errorf("inspect installed editor alias: %w", err)
	}
	// System alternatives commonly expose an "editor" symlink. Match its
	// installed target and use that editor's argument policy and executable.
	for _, candidate := range []string{"vim", "nvim", "nano", "emacs", "code", "subl", "notepad", "vi"} {
		executable, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		installedFile, err := os.Stat(executable)
		if err == nil && os.SameFile(selectedFile, installedFile) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("editor alias must select a supported editor installed in PATH")
}

// Split literal arguments without expansions, substitutions, or a shell.
func splitProfileEditor(value string) ([]string, error) {
	var (
		parts   []string
		part    strings.Builder
		quote   byte
		started bool
	)
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '\x00' || ch == '\n' || ch == '\r' {
			return nil, fmt.Errorf("editor command must not contain control characters")
		}
		if ch == '\\' && quote != '\'' && runtime.GOOS != "windows" {
			i++
			if i == len(value) {
				return nil, fmt.Errorf("unterminated escape sequence")
			}
			if value[i] == '\x00' || value[i] == '\n' || value[i] == '\r' {
				return nil, fmt.Errorf("editor command must not contain control characters")
			}
			part.WriteByte(value[i])
			started = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				part.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			started = true
		case ' ', '\t':
			if started {
				parts = append(parts, part.String())
				part.Reset()
				started = false
			}
		default:
			part.WriteByte(ch)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	if started {
		parts = append(parts, part.String())
	}
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("editor executable is required")
	}
	return parts, nil
}

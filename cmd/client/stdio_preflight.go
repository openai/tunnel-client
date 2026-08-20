package main

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const maxStdioPreflightDepth = 4

func preflightStdioCommand(raw string) (string, error) {
	args, err := parseStdioCommandArgv(raw)
	if err != nil {
		return "", err
	}
	return preflightStdioCommandArgs(args, 0)
}

func preflightStdioCommandArgs(args []string, depth int) (string, error) {
	if len(args) == 0 {
		return "", errors.New("command is empty")
	}
	resolved, err := preflightExecutableToken(args[0])
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err := preflightDirectScriptInterpreter(args[0], resolved); err != nil {
			return "", err
		}
	}
	if depth >= maxStdioPreflightDepth {
		return resolved, nil
	}
	if err := preflightWrappedStdioCommand(args, depth); err != nil {
		return "", err
	}
	return resolved, nil
}

func preflightExecutableToken(executable string) (string, error) {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return "", formatStdioPreflightError(executable, err)
	}
	if runtime.GOOS != "windows" {
		if err := ensureExecutableFile(executable, resolved); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func ensureExecutableFile(display string, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return formatStdioPreflightError(display, err)
	}
	if info.IsDir() {
		return fmt.Errorf("stdio MCP executable %q is a directory; fix: point mcp.command at a file, not %s", display, shellQuote(path))
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("stdio MCP executable %q is not executable; fix: chmod +x %s", display, shellQuote(path))
	}
	return nil
}

func preflightDirectScriptInterpreter(display string, path string) error {
	line, ok, err := readShebangLine(path)
	if err != nil {
		return fmt.Errorf("stdio MCP executable %q could not be inspected; fix: ensure %s is readable: %w", display, shellQuote(path), err)
	}
	if !ok {
		return nil
	}
	return preflightShebangInterpreter(path, line)
}

func readShebangLine(path string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() {
		_ = file.Close()
	}()

	buf := make([]byte, 4096)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	if n < 2 || string(buf[:2]) != "#!" {
		return "", false, nil
	}
	line := string(buf[2:n])
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSpace(line), true, nil
}

func preflightShebangInterpreter(scriptPath string, shebang string) error {
	args, err := parseStdioCommandArgv(shebang)
	if err != nil {
		return fmt.Errorf("stdio MCP script %q has an invalid shebang %q; fix: update the first line to an installed interpreter: %w", scriptPath, shebang, err)
	}
	if len(args) == 0 {
		return fmt.Errorf("stdio MCP script %q has an empty shebang; fix: update the first line to an installed interpreter", scriptPath)
	}
	if isEnvCommand(args[0]) {
		nested, err := envWrappedArgs(args)
		if err != nil {
			return fmt.Errorf("stdio MCP script %q has an invalid env shebang; fix: use #!/usr/bin/env <interpreter>: %w", scriptPath, err)
		}
		if _, err := preflightStdioCommandArgs(nested, maxStdioPreflightDepth); err != nil {
			return fmt.Errorf("stdio MCP script %q uses an unavailable interpreter via env; fix: install %q or update the shebang: %w", scriptPath, nested[0], err)
		}
		return nil
	}
	if !filepath.IsAbs(args[0]) {
		return fmt.Errorf("stdio MCP script %q uses non-absolute shebang interpreter %q; fix: use an absolute interpreter path or /usr/bin/env <interpreter>", scriptPath, args[0])
	}
	if _, err := preflightExecutableToken(args[0]); err != nil {
		return fmt.Errorf("stdio MCP script %q uses an unavailable interpreter %q; fix: install that interpreter or update the shebang: %w", scriptPath, args[0], err)
	}
	return nil
}

func preflightWrappedStdioCommand(args []string, depth int) error {
	switch commandBase(args[0]) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return preflightShellWrapper(args, depth)
	case "python", "python3", "node", "ruby", "perl", "php", "deno":
		return preflightInterpreterWrapper(args)
	case "env":
		nested, err := envWrappedArgs(args)
		if err != nil {
			return err
		}
		_, err = preflightStdioCommandArgs(nested, depth+1)
		return err
	default:
		return nil
	}
}

func preflightShellWrapper(args []string, depth int) error {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			i++
			if i < len(args) {
				return ensureReadableScript(args[i])
			}
			return nil
		}
		if isShellCommandStringOption(arg) {
			if i+1 >= len(args) {
				return fmt.Errorf("stdio MCP shell wrapper %q uses %s without a command string; fix: pass the command after %s", args[0], arg, arg)
			}
			nested, err := parseStdioCommandArgv(args[i+1])
			if err != nil {
				return fmt.Errorf("stdio MCP shell command %q could not be parsed: %w", args[i+1], err)
			}
			nested, err = normalizeShellCommandArgs(nested)
			if err != nil {
				return err
			}
			if len(nested) == 0 {
				return nil
			}
			_, err = preflightStdioCommandArgs(nested, depth+1)
			return err
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return ensureReadableScript(arg)
	}
	return nil
}

func preflightInterpreterWrapper(args []string) error {
	idx := interpreterScriptArgIndex(commandBase(args[0]), args)
	if idx < 0 {
		return nil
	}
	return ensureReadableScript(args[idx])
}

func interpreterScriptArgIndex(interpreter string, args []string) int {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if interpreterStopsBeforeScript(interpreter, arg) {
			return -1
		}
		if interpreterOptionConsumesValue(interpreter, arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return i
	}
	return -1
}

func interpreterStopsBeforeScript(interpreter string, arg string) bool {
	switch interpreter {
	case "python", "python3":
		return arg == "-m" || arg == "-c"
	case "node":
		return arg == "-e" || arg == "--eval" || strings.HasPrefix(arg, "--eval=")
	case "ruby", "perl", "php":
		return arg == "-e" || arg == "-E"
	case "deno":
		return arg == "eval"
	default:
		return false
	}
}

func interpreterOptionConsumesValue(interpreter string, arg string) bool {
	switch interpreter {
	case "node":
		return arg == "-r" || arg == "--require" || arg == "--loader" || arg == "--import"
	case "ruby":
		return arg == "-I" || arg == "-r"
	case "perl":
		return arg == "-I" || arg == "-M"
	case "php":
		return arg == "-c" || arg == "-d"
	case "deno":
		return arg == "--config" || arg == "--import-map" || arg == "--cert"
	default:
		return false
	}
}

func ensureReadableScript(script string) error {
	info, err := os.Stat(script)
	if err != nil {
		return fmt.Errorf("stdio MCP script %q was not found; fix: correct the script path in mcp.command or create the file before running tunnel-client: %w", script, err)
	}
	if info.IsDir() {
		return fmt.Errorf("stdio MCP script %q is a directory; fix: point the wrapper at a script file", script)
	}
	file, err := os.Open(script)
	if err != nil {
		return fmt.Errorf("stdio MCP script %q is not readable; fix: chmod +r %s: %w", script, shellQuote(script), err)
	}
	_ = file.Close()
	return nil
}

func envWrappedArgs(args []string) ([]string, error) {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("stdio MCP env wrapper %q does not name a command", args[0])
			}
			return args[i+1:], nil
		}
		if arg == "-S" || arg == "--split-string" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("stdio MCP env wrapper %q uses %s without a command string", args[0], arg)
			}
			if len(args[i+1:]) == 1 {
				return parseStdioCommandArgv(args[i+1])
			}
			return args[i+1:], nil
		}
		if strings.HasPrefix(arg, "-") {
			if envOptionConsumesValue(arg) {
				i++
			}
			continue
		}
		if isShellAssignment(arg) {
			continue
		}
		return args[i:], nil
	}
	return nil, fmt.Errorf("stdio MCP env wrapper %q does not name a command", args[0])
}

func normalizeShellCommandArgs(args []string) ([]string, error) {
	for len(args) > 0 {
		switch args[0] {
		case "exec", "command":
			args = args[1:]
			continue
		case "env":
			return envWrappedArgs(args)
		default:
			if isShellAssignment(args[0]) {
				args = args[1:]
				continue
			}
			return args, nil
		}
	}
	return args, nil
}

func envOptionConsumesValue(arg string) bool {
	return arg == "-u" || arg == "--unset" || arg == "-C" || arg == "--chdir"
}

func isShellCommandStringOption(arg string) bool {
	return strings.HasPrefix(arg, "-") && strings.Contains(arg[1:], "c")
}

func isEnvCommand(command string) bool {
	return commandBase(command) == "env"
}

func commandBase(command string) string {
	base := strings.ToLower(filepath.Base(command))
	return strings.TrimSuffix(base, ".exe")
}

func isShellAssignment(arg string) bool {
	idx := strings.IndexByte(arg, '=')
	if idx <= 0 {
		return false
	}
	for i := range idx {
		ch := arg[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' || (i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

func formatStdioPreflightError(executable string, err error) error {
	switch {
	case errors.Is(err, iofs.ErrPermission):
		candidate := executablePathCandidate(executable)
		return fmt.Errorf("stdio MCP executable %q is not executable; fix: chmod +x %s: %w", executable, shellQuote(candidate), err)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, iofs.ErrNotExist):
		return fmt.Errorf("stdio MCP executable %q was not found; fix: install it, use an absolute path, or ensure it is on PATH: %w", executable, err)
	default:
		return fmt.Errorf("stdio MCP executable %q is unavailable: %w", executable, err)
	}
}

func executablePathCandidate(executable string) string {
	if strings.ContainsRune(executable, os.PathSeparator) {
		return executable
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, executable)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return executable
}

func parseStdioCommandArgv(raw string) ([]string, error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return nil, errors.New("command is empty")
	}
	var (
		args     []string
		builder  strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)

	for i := 0; i < len(input); i++ {
		ch := input[i]
		if escaped {
			builder.WriteByte(ch)
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
				continue
			}
			builder.WriteByte(ch)
			continue
		}
		if inDouble {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
			default:
				builder.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t', '\n', '\r':
			if builder.Len() > 0 {
				args = append(args, builder.String())
				builder.Reset()
			}
		default:
			builder.WriteByte(ch)
		}
	}

	if escaped {
		return nil, errors.New("unterminated escape sequence")
	}
	if inSingle || inDouble {
		return nil, errors.New("unterminated quoted string")
	}
	if builder.Len() > 0 {
		args = append(args, builder.String())
	}
	if len(args) == 0 {
		return nil, errors.New("command is empty")
	}
	return args, nil
}

func shellQuote(path string) string {
	if path == "" {
		return "''"
	}
	if !strings.ContainsAny(path, " \t\n'\"\\$`") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitProfileEditor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		input string
		want  []string
	}{
		{"simple", "vim -f -n", []string{"vim", "-f", "-n"}},
		{"double quoted path", `"/path with spaces/code" --wait`, []string{"/path with spaces/code", "--wait"}},
		{"single quoted path", `'/path with spaces/vim' '-f'`, []string{"/path with spaces/vim", "-f"}},
		{"literal metacharacters", `'/path;$(id)/vim'`, []string{"/path;$(id)/vim"}},
		{"empty argument", `vim ""`, []string{"vim", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitProfileEditor(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, value := range []string{"", " \t ", `""`, `"vim`, "vim\n-f", "vim\x00", "vim\r-f"} {
		t.Run("invalid_"+value, func(t *testing.T) {
			_, err := splitProfileEditor(value)
			require.Error(t, err)
		})
	}
	if runtime.GOOS == "windows" {
		parts, err := splitProfileEditor(`"C:\Program Files\Vim\vim.exe" -f`)
		require.NoError(t, err)
		require.Equal(t, []string{`C:\Program Files\Vim\vim.exe`, "-f"}, parts)
	} else {
		parts, err := splitProfileEditor(`/path\ with\ spaces/vim -f`)
		require.NoError(t, err)
		require.Equal(t, []string{"/path with spaces/vim", "-f"}, parts)
		_, err = splitProfileEditor(`vim\`)
		require.ErrorContains(t, err, "unterminated escape")
	}
}

func TestRunProfileEditorRejectsUnsafeCommandsBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{"sh", "-c id"},
		{"bash", "-c id"},
		{"python3", "-c pass"},
		{"env", "vim"},
		{"vim", `-c 'silent !id'`},
		{"vim", `--cmd 'silent !id'`},
		{"vim", `+!id`},
		{"vim", `-S /tmp/script`},
		{"nvim", `--headless -c quit`},
		{"emacs", `--eval '(kill-emacs)'`},
		{"emacs", `--script /tmp/script`},
		{"code", `--extensions-dir /tmp/extensions`},
		{"nano", `--rcfile /tmp/config`},
		{"vim", `; id`},
		{"vim", `$(id)`},
		{"vim", "`id`"},
		{"vim", `""`},
		{"vim", `unexpected.yaml`},
	} {
		t.Run(tc.name+"/"+tc.args, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "launched")
			editor := writeProfileEditorScript(t, filepath.Join(dir, tc.name), "touch "+quoteProfileEditorTestPath(marker)+"\n")
			err := runProfileEditor(filepath.Join(dir, "profile.yaml"), profileEditorTestEnv(map[string]string{"EDITOR": editor + " " + tc.args}))
			require.Error(t, err)
			_, err = os.Stat(marker)
			require.ErrorIs(t, err, os.ErrNotExist, "rejected editor must not start")
		})
	}
}

func TestRunProfileEditorPassesLiteralArguments(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "arguments")
	editor := writeProfileEditorScript(t, filepath.Join(dir, "editor path;$(id)", "code"), "printf '%s\\n' \"$@\" > "+quoteProfileEditorTestPath(output)+"\n")
	profilePath := filepath.Join(dir, "+profile;$(id) ' with spaces.yaml")
	require.NoError(t, runProfileEditor(profilePath, profileEditorTestEnv(map[string]string{"EDITOR": editor + " --wait --reuse-window"})))
	got, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "--wait\n--reuse-window\n"+profilePath+"\n", string(got))
}

func TestRunProfileEditorSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		visual string
		editor string
		want   string
	}{
		{"visual first", "selected", "unsupported", "visual"},
		{"empty visual falls back", " \t ", "selected", "editor"},
		{"unset visual falls back", "", "selected", "editor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "selection")
			selected := writeProfileEditorScript(t, filepath.Join(dir, "vim"), "printf '%s' "+tc.want+" > "+quoteProfileEditorTestPath(marker)+"\n")
			env := map[string]string{"VISUAL": tc.visual, "EDITOR": tc.editor}
			for key, value := range env {
				if value == "selected" {
					env[key] = selected
				}
			}
			require.NoError(t, runProfileEditor(filepath.Join(dir, "profile.yaml"), profileEditorTestEnv(env)))
			got, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
	require.ErrorContains(t, runProfileEditor("profile.yaml", profileEditorTestEnv(nil)), "set VISUAL or EDITOR")
}

func TestProfileEditorCommandMakesFilenameAbsolute(t *testing.T) {
	editor := writeProfileEditorScript(t, filepath.Join(t.TempDir(), "vim"), "exit 0\n")
	cmd, err := profileEditorCommand(editor, "+command.yaml")
	require.NoError(t, err)
	want, err := filepath.Abs("+command.yaml")
	require.NoError(t, err)
	require.Equal(t, []string{cmd.Path, want}, cmd.Args)
}

func TestProfileEditorCommandSupportsCommonEditorOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{"vi", "-R"},
		{"vim", "-f -n"},
		{"nvim", "-f"},
		{"nano", "--linenumbers -w"},
		{"emacs", "-nw -Q"},
		{"code", "--wait --reuse-window"},
		{"subl", "--wait --new-window"},
		{"notepad", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			editor := writeProfileEditorScript(t, filepath.Join(dir, tc.name), "exit 0\n")
			profile := filepath.Join(dir, "profile.yaml")
			cmd, err := profileEditorCommand(editor+" "+tc.args, profile)
			require.NoError(t, err)
			wantArgs := append(strings.Fields(tc.args), profile)
			require.Equal(t, wantArgs, cmd.Args[1:])
		})
	}
}

func TestRunProfileEditorRejectsUninstalledPathBeforeLaunch(t *testing.T) {
	dir := t.TempDir()
	writeProfileEditorScript(t, filepath.Join(dir, "installed", "vim"), "exit 0\n")
	marker := filepath.Join(dir, "launched")
	uninstalled := filepath.Join(dir, "vim")
	require.NoError(t, os.WriteFile(uninstalled, []byte("#!/bin/sh\ntouch "+quoteProfileEditorTestPath(marker)+"\n"), 0o700))
	err := runProfileEditor(filepath.Join(dir, "profile.yaml"), profileEditorTestEnv(map[string]string{"EDITOR": quoteProfileEditorTestPath(uninstalled)}))
	require.ErrorContains(t, err, "must select the installed")
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestProfileEditorCommandAcceptsInstalledExecutableAlias(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "installed", "vim")
	writeProfileEditorScript(t, installed, "exit 0\n")
	alias := filepath.Join(dir, "vim")
	require.NoError(t, os.Symlink(installed, alias))
	for _, editor := range []string{"vim", quoteProfileEditorTestPath(alias)} {
		cmd, err := profileEditorCommand(editor, filepath.Join(dir, "profile.yaml"))
		require.NoError(t, err)
		require.Equal(t, installed, cmd.Path)
	}
}

func TestRunProfileEditorPreservesSystemEditorAlias(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "arguments")
	installed := filepath.Join(dir, "vim")
	writeProfileEditorScript(t, installed, "printf '%s\\n' \"$@\" > "+quoteProfileEditorTestPath(output)+"\n")
	alias := filepath.Join(dir, "editor")
	require.NoError(t, os.Symlink(installed, alias))
	profile := filepath.Join(dir, "legacy profile.yaml")
	for _, selected := range []string{"editor", quoteProfileEditorTestPath(alias)} {
		require.NoError(t, runProfileEditor(profile, profileEditorTestEnv(map[string]string{"EDITOR": selected + " -f"})))
		got, err := os.ReadFile(output)
		require.NoError(t, err)
		require.Equal(t, "-f\n"+profile+"\n", string(got))
		cmd, err := profileEditorCommand(selected+" -f", profile)
		require.NoError(t, err)
		require.Equal(t, installed, cmd.Path)
	}
	require.NoError(t, os.Remove(output))
	err := runProfileEditor(profile, profileEditorTestEnv(map[string]string{"EDITOR": "editor -c 'silent !id'"}))
	require.ErrorContains(t, err, "unsupported option")
	_, err = os.Stat(output)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunProfileEditorRejectsUnapprovedSystemEditorAlias(t *testing.T) {
	dir := t.TempDir()
	writeProfileEditorScript(t, filepath.Join(dir, "vim"), "exit 0\n")
	marker := filepath.Join(dir, "launched")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "editor"), []byte("#!/bin/sh\ntouch "+quoteProfileEditorTestPath(marker)+"\n"), 0o700))
	err := runProfileEditor(filepath.Join(dir, "profile.yaml"), profileEditorTestEnv(map[string]string{"EDITOR": "editor"}))
	require.ErrorContains(t, err, "editor alias must select a supported editor")
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// PATH changes require serial tests; this also avoids ETXTBSY when another test
// forks while an executable fixture is being written.
func writeProfileEditorScript(t *testing.T, path, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test editor uses a Unix executable script")
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700))
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return quoteProfileEditorTestPath(path)
}

func quoteProfileEditorTestPath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}

func profileEditorTestEnv(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

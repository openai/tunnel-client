# Profile Editors

`tunnel-client profiles edit <name>` selects the first nonempty value of
`VISUAL`, then `EDITOR`. The value must name an installed supported editor and
may include only the options listed below. Quote executable paths containing
spaces, for example `EDITOR='"/Applications/My Editor/bin/code" --wait'`.
The editor is resolved by its supported name through `PATH`. An explicit path
must refer to that same installed executable, including through a symlink.
The conventional `editor` alias is also accepted when it resolves to one of
these installed editors; its target's option policy applies.

| Editor | Accepted options |
| --- | --- |
| `vi` | `-R`, `-n` |
| `vim`, `nvim` | `-f`, `--nofork`, `-n`, `-R` |
| `nano` | `-w`, `--nowrap`, `-l`, `--linenumbers`, `-m`, `--mouse`, `-E`, `--tabstospaces` |
| `emacs` | `-nw`, `--no-window-system`, `-Q`, `--quick`, `-q`, `--no-init-file`, `--no-site-file` |
| `code` | `-w`, `--wait`, `-n`, `--new-window`, `-r`, `--reuse-window`, `--disable-extensions` |
| `subl` | `-w`, `--wait`, `-n`, `--new-window`, `-b`, `--background` |
| `notepad` | None |

Windows editor executables may use the `.exe` extension. Single and double
quotes group arguments; Unix backslashes escape the following character.
There is no environment-variable expansion, command substitution, or shell
execution. The profile filename is appended as one absolute argument.

Interpreter commands, wrappers such as `env`, extra filenames, and editor
evaluation options such as `-c`, `--cmd`, `--eval`, and `+command` are rejected
before any process starts. Use `code --wait` or `subl --wait` so the editor
finishes before the profile is validated and saved.

This policy limits what the editor environment value can request; naming an
unrelated executable `vim` does not make an explicit path acceptable. The editor
installed in `PATH`, its startup configuration and extensions, and the local
executable search path remain trusted. Executable contents and provenance are
not verified.

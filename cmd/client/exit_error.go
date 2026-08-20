package main

type silentExitError struct {
	code int
}

func (e silentExitError) Error() string {
	return ""
}

func (e silentExitError) ExitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
}

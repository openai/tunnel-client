package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	rootCmd := newRootCommand(os.LookupEnv, os.Stdout, os.Stderr)
	ctx, stop := newRootCommandContext()
	defer stop()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		exitCode := 1
		if coded, ok := err.(interface{ ExitCode() int }); ok {
			exitCode = coded.ExitCode()
		}
		if err.Error() != "" {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(exitCode)
	}
}

func newRootCommandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	processLifecycleHelperArg        = "--codex-bridge-process-lifecycle-helper"
	processLifecycleModeBlockAccount = "block-account"
	processLifecycleModeExit         = "exit"
	processLifecycleModeExitReady    = "exit-ready"
	processLifecycleModeFail         = "fail"
	processLifecycleModeReady        = "ready"
	processLifecycleModeRecover      = "recover"
)

func TestBridgeProcessLifecycleHelper(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == processLifecycleHelperArg {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	if len(os.Args) <= marker+2 {
		os.Exit(2)
	}

	pidFile, mode := os.Args[marker+1], os.Args[marker+2]
	previous, err := os.ReadFile(pidFile)
	if err != nil && !os.IsNotExist(err) {
		os.Exit(2)
	}
	attempt := len(strings.Fields(string(previous))) + 1
	pidWriter, err := os.OpenFile(pidFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(pidWriter, os.Getpid()); err != nil {
		_ = pidWriter.Close()
		os.Exit(2)
	}
	if err := pidWriter.Close(); err != nil {
		os.Exit(2)
	}
	if mode == processLifecycleModeExit {
		os.Exit(0)
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || len(request.ID) == 0 {
			continue
		}
		if request.Method == "account/read" && mode == processLifecycleModeBlockAccount {
			if err := encoder.Encode(map[string]any{
				"method": "test/initialization-blocked",
			}); err != nil {
				os.Exit(2)
			}
			continue
		}

		response := map[string]any{
			"id":     request.ID,
			"result": map[string]any{},
		}
		if request.Method == "account/read" &&
			(mode == processLifecycleModeFail || mode == processLifecycleModeRecover && attempt <= 2) {
			delete(response, "result")
			response["error"] = map[string]any{
				"code":    -32000,
				"message": "test initialization failure",
			}
		} else if request.Method == "account/read" {
			response["result"] = map[string]any{
				"account": map[string]any{
					"type":     "chatgpt",
					"email":    "test@example.com",
					"planType": "test",
				},
				"requiresOpenaiAuth": false,
			}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(2)
		}
		if request.Method == "account/read" && mode == processLifecycleModeExitReady {
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func TestBridgeInitializationFailureReapsChild(t *testing.T) {
	t.Parallel()

	bridge, pidFile := newProcessLifecycleTestBridge(t, processLifecycleModeFail)

	err := bridge.startProcessLocked()
	require.ErrorContains(t, err, "test initialization failure")

	pids := readProcessLifecyclePIDs(t, pidFile)
	require.Len(t, pids, 1)
	pid := pids[0]
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH,
		"initialization returned while failed child %d was still alive", pid)
}

func TestBridgeFailedStartupSetsRetryDelay(t *testing.T) {
	t.Parallel()

	bridge, _ := newProcessLifecycleTestBridge(t, processLifecycleModeFail)
	started := time.Now()
	done := make(chan struct{})
	bridge.startProcess(done)
	<-done

	bridge.mu.RLock()
	retryAfter := bridge.retryAfter
	lastError := bridge.lastError
	bridge.mu.RUnlock()
	require.False(t, retryAfter.Before(started.Add(startupRetryDelay)))
	require.Contains(t, lastError, "test initialization failure")
}

func TestBridgeUnexpectedExitSetsRetryDelay(t *testing.T) {
	t.Parallel()

	bridge, _ := newProcessLifecycleTestBridge(t, processLifecycleModeExitReady)
	started := time.Now()
	startupDone := make(chan struct{})
	bridge.startProcess(startupDone)
	<-startupDone

	bridge.mu.RLock()
	waitDone := bridge.waitDone
	bridge.mu.RUnlock()
	require.NotNil(t, waitDone)
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initialized child to exit")
	case <-waitDone:
	}

	bridge.mu.RLock()
	retryAfter := bridge.retryAfter
	lastError := bridge.lastError
	ready := bridge.ready
	running := bridge.running
	bridge.mu.RUnlock()
	require.False(t, retryAfter.Before(started.Add(startupRetryDelay)))
	require.Equal(t, "codex app-server exited", lastError)
	require.False(t, ready)
	require.False(t, running)
}

func TestBridgeConcurrentStartupRecoversWithoutLeaking(t *testing.T) {
	t.Parallel()

	bridge, pidFile := newProcessLifecycleTestBridge(t, processLifecycleModeRecover)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var callersDone sync.WaitGroup
	callersDone.Add(callers)
	for range callers {
		go func() {
			defer callersDone.Done()
			<-start
			errs <- bridge.EnsureStarted(ctx)
		}()
	}
	close(start)
	waitDone := make(chan struct{})
	go func() {
		callersDone.Wait()
		close(waitDone)
	}()
	select {
	case <-ctx.Done():
		t.Fatal("timed out waiting for concurrent startup")
	case <-waitDone:
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	pids := readProcessLifecyclePIDs(t, pidFile)
	require.Len(t, pids, 3)
	for _, pid := range pids[:2] {
		require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH,
			"failed startup child %d was not reaped", pid)
	}
	snapshot := bridge.Snapshot()
	require.True(t, snapshot.Running)
	require.True(t, snapshot.Ready)
	require.Equal(t, pids[2], snapshot.PID)

	require.NoError(t, bridge.Stop(ctx))
	require.ErrorIs(t, syscall.Kill(pids[2], 0), syscall.ESRCH,
		"ready child %d was not reaped during shutdown", pids[2])
	spawnCount := len(readProcessLifecyclePIDs(t, pidFile))
	require.ErrorContains(t, bridge.EnsureStarted(ctx), "shutting down")
	require.Len(t, readProcessLifecyclePIDs(t, pidFile), spawnCount)
}

func TestBridgeShutdownDuringInitializationReapsChild(t *testing.T) {
	t.Parallel()

	bridge, pidFile := newProcessLifecycleTestBridge(t, processLifecycleModeBlockAccount)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := bridge.Subscribe(ctx)
	startErr := make(chan error, 1)
	go func() {
		startErr <- bridge.EnsureStarted(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for blocked initialization")
		case event := <-events:
			if event.Method == "test/initialization-blocked" {
				goto blocked
			}
		}
	}

blocked:
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, bridge.Stop(stopCtx))
	select {
	case <-ctx.Done():
		t.Fatal("timed out waiting for startup to observe shutdown")
	case err := <-startErr:
		require.ErrorContains(t, err, "shutting down")
	}

	pids := readProcessLifecyclePIDs(t, pidFile)
	require.Len(t, pids, 1)
	require.ErrorIs(t, syscall.Kill(pids[0], 0), syscall.ESRCH)
	snapshot := bridge.Snapshot()
	require.False(t, snapshot.Starting)
	require.False(t, snapshot.Running)
	require.False(t, snapshot.Ready)
	require.Zero(t, snapshot.PID)
}

func TestBridgeCannotStartAfterShutdown(t *testing.T) {
	t.Parallel()

	bridge, pidFile := newProcessLifecycleTestBridge(t, processLifecycleModeReady)
	require.NoError(t, bridge.Stop(context.Background()))
	require.ErrorContains(t, bridge.EnsureStarted(context.Background()), "shutting down")
	require.ErrorContains(t, bridge.startProcessLocked(), "shutting down")
	_, err := os.Stat(pidFile)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestBridgeStaleExitPreservesCurrentProcess(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	require.NoError(t, err)
	old := exec.Command(
		executable,
		"-test.run=^TestBridgeProcessLifecycleHelper$",
		"--",
		processLifecycleHelperArg,
		filepath.Join(t.TempDir(), "old.pid"),
		processLifecycleModeExit,
	)
	require.NoError(t, old.Start())

	bridge := NewBridge(nil, nil)
	current := &exec.Cmd{}
	currentWaitDone := make(chan struct{})
	bridge.mu.Lock()
	bridge.cmd = current
	bridge.pid = 4242
	bridge.stdin = discardWriteCloser{}
	bridge.waitDone = currentWaitDone
	bridge.running = true
	bridge.ready = true
	bridge.pending[42] = pendingRequest{ch: make(chan rpcEnvelope, 1)}
	bridge.mu.Unlock()

	oldWaitDone := make(chan struct{})
	go bridge.waitForExit(old, oldWaitDone)
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale process exit")
	case <-oldWaitDone:
	}

	bridge.mu.RLock()
	actualCmd := bridge.cmd
	actualPID := bridge.pid
	actualWaitDone := bridge.waitDone
	running := bridge.running
	ready := bridge.ready
	_, pendingPreserved := bridge.pending[42]
	bridge.mu.RUnlock()
	require.Same(t, current, actualCmd)
	require.Equal(t, 4242, actualPID)
	require.True(t, currentWaitDone == actualWaitDone)
	require.True(t, running)
	require.True(t, ready)
	require.True(t, pendingPreserved)
}

func newProcessLifecycleTestBridge(t *testing.T, mode string) (*Bridge, string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	bridge := NewBridge(nil, nil)
	bridge.cfg = commandConfig{
		command: executable,
		args: []string{
			"-test.run=^TestBridgeProcessLifecycleHelper$",
			"--",
			processLifecycleHelperArg,
			pidFile,
			mode,
		},
		cwd: t.TempDir(),
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bridge.Stop(ctx)
		for _, pid := range processLifecyclePIDs(pidFile) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return bridge, pidFile
}

func readProcessLifecyclePIDs(t *testing.T, pidFile string) []int {
	t.Helper()
	pids := processLifecyclePIDs(pidFile)
	require.NotEmpty(t, pids)
	for _, pid := range pids {
		require.Positive(t, pid, fmt.Sprintf("invalid PID in %s", pidFile))
	}
	return pids
}

func processLifecyclePIDs(pidFile string) []int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return nil
	}
	pids := make([]int, 0, len(strings.Fields(string(data))))
	for value := range strings.FieldsSeq(string(data)) {
		pid, err := strconv.Atoi(value)
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

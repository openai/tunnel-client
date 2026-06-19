package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"go.openai.org/api/tunnel-client/pkg/codexappserver"
)

const (
	defaultCodexAssistantApprovalPolicy = "never"
	defaultCodexAssistantSandboxType    = "workspace-write"
	defaultCodexAssistantEffort         = "medium"
	defaultCodexAssistantLoginTimeout   = 5 * time.Minute
)

var codexAssistantTurnIdleTimeout = 2 * time.Minute

type codexAssistantOptions struct {
	CWD                   string
	Model                 string
	ModelProvider         string
	ApprovalPolicy        string
	SandboxType           string
	DeveloperInstructions string
	Effort                string
	Summary               string
	LoginTimeout          time.Duration
}

type codexAssistantWaitingRenderer struct {
	stderr      io.Writer
	isTerminal  bool
	stop        chan struct{}
	done        chan struct{}
	mu          sync.Mutex
	promptShown bool
}

func newCodexAssistantCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	options := codexAssistantOptions{
		ApprovalPolicy: defaultCodexAssistantApprovalPolicy,
		SandboxType:    defaultCodexAssistantSandboxType,
		Effort:         defaultCodexAssistantEffort,
		LoginTimeout:   defaultCodexAssistantLoginTimeout,
	}
	cmd := &cobra.Command{
		Use:   "assistant [prompt...]",
		Short: "Run a CLI assistant session through codex app-server",
		Long: strings.TrimSpace(`
Start a tunnel-client-backed assistant session in the terminal.

Pass a prompt as arguments for a one-shot response. Run without a prompt in a
TTY to enter REPL mode. When stdin is piped, the command reads the full stdin
payload as a single prompt.
`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodexAssistant(cmd, stdout, stderr, options, args)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&options.CWD, "cwd", "", "Working directory to attach to the assistant thread (defaults to the current directory)")
	cmd.Flags().StringVar(&options.Model, "model", "", "Optional Codex model override")
	cmd.Flags().StringVar(&options.ModelProvider, "model-provider", "", "Optional model provider override")
	cmd.Flags().StringVar(&options.ApprovalPolicy, "approval-policy", defaultCodexAssistantApprovalPolicy, "Approval policy to send to codex app-server")
	cmd.Flags().StringVar(&options.SandboxType, "sandbox-type", defaultCodexAssistantSandboxType, "Sandbox type to send to codex app-server")
	cmd.Flags().StringVar(&options.DeveloperInstructions, "developer-instructions", "", "Extra developer instructions appended to the assistant thread")
	cmd.Flags().StringVar(&options.Effort, "effort", defaultCodexAssistantEffort, "Codex effort override for turns")
	cmd.Flags().StringVar(&options.Summary, "summary", "", "Optional Codex summary mode override for turns")
	cmd.Flags().DurationVar(&options.LoginTimeout, "login-timeout", defaultCodexAssistantLoginTimeout, "How long to wait for device-code login when Codex is logged out")
	return cmd
}

func runCodexAssistant(
	cmd *cobra.Command,
	stdout io.Writer,
	stderr io.Writer,
	options codexAssistantOptions,
	args []string,
) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	bridge := codexappserver.NewBridge(nil, nil)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = bridge.Stop(stopCtx)
	}()

	if err := bridge.EnsureStarted(ctx); err != nil {
		return fmt.Errorf("start codex app-server bridge: %w", err)
	}

	interactive := stdinIsTerminal(cmd.InOrStdin())
	if err := ensureCodexAssistantLogin(ctx, bridge, stderr, interactive, options.LoginTimeout); err != nil {
		return err
	}

	initialPrompt := strings.TrimSpace(strings.Join(args, " "))
	switch {
	case initialPrompt != "":
		return runCodexAssistantOneShot(ctx, bridge, stdout, stderr, options, initialPrompt)
	case !interactive:
		prompt, err := readCodexAssistantPrompt(cmd.InOrStdin())
		if err != nil {
			return err
		}
		if prompt == "" {
			return errors.New("assistant prompt is required when stdin is empty")
		}
		return runCodexAssistantOneShot(ctx, bridge, stdout, stderr, options, prompt)
	default:
		return runCodexAssistantREPL(ctx, bridge, cmd.InOrStdin(), stdout, stderr, options)
	}
}

func ensureCodexAssistantLogin(
	ctx context.Context,
	bridge *codexappserver.Bridge,
	stderr io.Writer,
	interactive bool,
	timeout time.Duration,
) error {
	snapshot := bridge.Snapshot()
	if snapshot.Account != nil && strings.TrimSpace(snapshot.Account.Type) != "" {
		return nil
	}
	if snapshot.RequiresOpenAIAuth == nil || !*snapshot.RequiresOpenAIAuth {
		return nil
	}
	if !interactive {
		return errors.New("Codex is logged out; rerun `tunnel-client codex assistant` in a terminal to complete device-code login")
	}

	login, err := bridge.StartDeviceCodeLogin(ctx)
	if err != nil {
		return fmt.Errorf("start Codex device-code login: %w", err)
	}
	_, _ = fmt.Fprintln(stderr, "Codex login required.")
	_, _ = fmt.Fprintf(stderr, "Open: %s\n", login.VerificationURL)
	_, _ = fmt.Fprintf(stderr, "Code: %s\n", login.UserCode)
	_, _ = fmt.Fprintf(stderr, "Waiting for login completion (timeout %s)...\n", timeout)

	waitCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		current := bridge.Snapshot()
		if current.Account != nil && strings.TrimSpace(current.Account.Type) != "" {
			_, _ = fmt.Fprintln(stderr, "Codex login complete.")
			return nil
		}
		if current.Login != nil && !current.Login.Pending && strings.TrimSpace(current.Login.LastError) != "" {
			return fmt.Errorf("Codex login failed: %s", current.Login.LastError)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timed out waiting for Codex login")
		case <-ticker.C:
		}
	}
}

func runCodexAssistantOneShot(
	ctx context.Context,
	bridge *codexappserver.Bridge,
	stdout io.Writer,
	stderr io.Writer,
	options codexAssistantOptions,
	prompt string,
) error {
	threadID, err := startCodexAssistantThread(ctx, bridge, options)
	if err != nil {
		return err
	}
	return runCodexAssistantPrompt(ctx, bridge, stdout, stderr, threadID, options, prompt)
}

func runCodexAssistantREPL(
	ctx context.Context,
	bridge *codexappserver.Bridge,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	options codexAssistantOptions,
) error {
	_, _ = fmt.Fprintln(stderr, "Starting Codex assistant. Type /exit to quit, /new to start a fresh chat, and /model to inspect or change model/reasoning.")
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)

	threadID := ""
	for {
		_, _ = fmt.Fprint(stderr, "you> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(stderr)
			return nil
		}
		prompt := strings.TrimSpace(scanner.Text())
		switch prompt {
		case "":
			continue
		case "/exit", "/quit":
			return nil
		case "/new":
			threadID = ""
			_, _ = fmt.Fprintln(stderr, "Starting a fresh chat on the next prompt.")
			continue
		}
		if handled, err := handleCodexAssistantSlashCommand(stderr, &options, prompt); handled {
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "model> %s\n", err)
			}
			continue
		}
		if threadID == "" {
			var err error
			threadID, err = startCodexAssistantThread(ctx, bridge, options)
			if err != nil {
				return err
			}
		}
		if err := runCodexAssistantPrompt(ctx, bridge, stdout, stderr, threadID, options, prompt); err != nil {
			return err
		}
	}
}

func startCodexAssistantThread(
	ctx context.Context,
	bridge *codexappserver.Bridge,
	options codexAssistantOptions,
) (string, error) {
	workingDir := assistantWorkingDirectory(options.CWD)
	result, err := bridge.StartThread(ctx, codexappserver.ThreadStartParams{
		CWD:                   workingDir,
		Model:                 strings.TrimSpace(options.Model),
		ModelProvider:         strings.TrimSpace(options.ModelProvider),
		ApprovalPolicy:        strings.TrimSpace(options.ApprovalPolicy),
		SandboxType:           strings.TrimSpace(options.SandboxType),
		DeveloperInstructions: buildCodexCLIDeveloperInstructions(workingDir, strings.TrimSpace(options.DeveloperInstructions)),
	})
	if err != nil {
		return "", fmt.Errorf("start assistant thread: %w", err)
	}
	return result.ThreadID, nil
}

func handleCodexAssistantSlashCommand(
	stderr io.Writer,
	options *codexAssistantOptions,
	prompt string,
) (bool, error) {
	prompt = strings.TrimSpace(prompt)
	if !strings.HasPrefix(prompt, "/") {
		return false, nil
	}
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return false, nil
	}
	switch fields[0] {
	case "/model":
		return true, handleCodexAssistantModelCommand(stderr, options, fields[1:])
	default:
		return false, nil
	}
}

func handleCodexAssistantModelCommand(
	stderr io.Writer,
	options *codexAssistantOptions,
	args []string,
) error {
	if options == nil {
		return errors.New("assistant options are required")
	}
	switch len(args) {
	case 0:
		_, _ = fmt.Fprintf(stderr, "model> model=%s reasoning=%s\n", codexAssistantValueOrCurrent(options.Model, "default"), codexAssistantValueOrCurrent(options.Effort, defaultCodexAssistantEffort))
		return nil
	case 1:
		if isCodexAssistantReasoning(args[0]) {
			options.Effort = strings.ToLower(strings.TrimSpace(args[0]))
			_, _ = fmt.Fprintf(stderr, "model> model=%s reasoning=%s\n", codexAssistantValueOrCurrent(options.Model, "default"), options.Effort)
			return nil
		}
		options.Model = strings.TrimSpace(args[0])
		_, _ = fmt.Fprintf(stderr, "model> model=%s reasoning=%s\n", codexAssistantValueOrCurrent(options.Model, "default"), codexAssistantValueOrCurrent(options.Effort, defaultCodexAssistantEffort))
		return nil
	default:
		model := strings.TrimSpace(args[0])
		reasoning := strings.ToLower(strings.TrimSpace(args[1]))
		if !isCodexAssistantReasoning(reasoning) {
			return fmt.Errorf("unknown reasoning %q; expected one of: low, medium, high", reasoning)
		}
		if model == "-" {
			model = options.Model
		}
		options.Model = model
		options.Effort = reasoning
		_, _ = fmt.Fprintf(stderr, "model> model=%s reasoning=%s\n", codexAssistantValueOrCurrent(options.Model, "default"), options.Effort)
		return nil
	}
}

func codexAssistantValueOrCurrent(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func isCodexAssistantReasoning(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func runCodexAssistantPrompt(
	ctx context.Context,
	bridge *codexappserver.Bridge,
	stdout io.Writer,
	stderr io.Writer,
	threadID string,
	options codexAssistantOptions,
	prompt string,
) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("assistant prompt is required")
	}
	workingDir := assistantWorkingDirectory(options.CWD)
	if item := buildCodexAssistantKnowledgeItem(prompt); item != nil {
		if err := bridge.InjectThreadItems(ctx, threadID, []map[string]any{item}); err != nil {
			return fmt.Errorf("inject assistant knowledge base context: %w", err)
		}
	}

	eventsCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := bridge.Subscribe(eventsCtx)

	result, err := bridge.StartTurn(ctx, codexappserver.TurnStartParams{
		ThreadID:       threadID,
		Input:          []map[string]any{buildCodexCLITextInput(prompt)},
		CWD:            workingDir,
		ApprovalPolicy: strings.TrimSpace(options.ApprovalPolicy),
		SandboxType:    strings.TrimSpace(options.SandboxType),
		Model:          strings.TrimSpace(options.Model),
		Effort:         strings.TrimSpace(options.Effort),
		Summary:        strings.TrimSpace(options.Summary),
	})
	if err != nil {
		return fmt.Errorf("start assistant turn: %w", err)
	}

	waiting := newCodexAssistantWaitingRenderer(stderr)
	return waitForCodexAssistantTurn(ctx, bridge, stdout, result.TurnID, events, waiting)
}

func waitForCodexAssistantTurn(
	ctx context.Context,
	bridge *codexappserver.Bridge,
	stdout io.Writer,
	turnID string,
	events <-chan codexappserver.Event,
	waiting *codexAssistantWaitingRenderer,
) error {
	if waiting != nil {
		defer waiting.Finish()
	}
	stallTimer := time.NewTimer(codexAssistantTurnIdleTimeout)
	defer stallTimer.Stop()
	resetStallTimer := func() {
		if !stallTimer.Stop() {
			select {
			case <-stallTimer.C:
			default:
			}
		}
		stallTimer.Reset(codexAssistantTurnIdleTimeout)
	}
	streamed := false
	finalMessage := ""
	for {
		select {
		case <-ctx.Done():
			return codexAssistantWaitError("assistant turn canceled", bridge, ctx.Err().Error())
		case <-stallTimer.C:
			return codexAssistantWaitError(
				"assistant turn stalled after turn/start",
				bridge,
				fmt.Sprintf("turn %s produced no completion or output for %s", turnID, codexAssistantTurnIdleTimeout),
			)
		case event, ok := <-events:
			if !ok {
				return codexAssistantWaitError("assistant event stream closed unexpectedly", bridge, "")
			}
			if event.TurnID != "" && event.TurnID != turnID {
				continue
			}
			if event.TurnID == turnID {
				resetStallTimer()
			}
			switch event.Method {
			case "process/error", "process/exited":
				detail := strings.TrimSpace(event.Summary)
				if detail == "" {
					detail = "codex app-server stopped while the assistant turn was in progress"
				}
				return codexAssistantWaitError("assistant bridge stopped", bridge, detail)
			case "error":
				detail := strings.TrimSpace(event.Summary)
				if detail == "" {
					detail = "codex app-server reported a turn error"
				}
				return codexAssistantWaitError("assistant turn failed", bridge, detail)
			case "item/agentMessage/delta":
				if event.Delta != "" {
					if waiting != nil {
						waiting.ShowPrompt()
					}
					_, _ = fmt.Fprint(stdout, event.Delta)
					streamed = true
				}
			case "item/completed":
				if text := codexAssistantCompletedMessage(event); text != "" {
					finalMessage = text
				}
			case "turn/completed":
				resetStallTimer()
				snapshot := bridge.Snapshot()
				if !streamed && finalMessage != "" {
					if waiting != nil {
						waiting.ShowPrompt()
					}
					_, _ = fmt.Fprint(stdout, finalMessage)
					streamed = true
				}
				if streamed {
					_, _ = fmt.Fprintln(stdout)
				}
				if snapshot.Turn != nil && snapshot.Turn.ID == turnID && snapshot.Turn.Status == "failed" {
					if strings.TrimSpace(snapshot.Turn.Error) != "" {
						return errors.New(snapshot.Turn.Error)
					}
					return errors.New("assistant turn failed")
				}
				return nil
			}
		}
	}
}

func codexAssistantWaitError(stage string, bridge *codexappserver.Bridge, detail string) error {
	message := strings.TrimSpace(stage)
	if text := strings.TrimSpace(detail); text != "" {
		message += ": " + text
	}
	if bridge != nil {
		if diagnostics := strings.TrimSpace(bridge.RecentDiagnosticSummary(6)); diagnostics != "" {
			message += "; " + diagnostics
		}
	}
	return errors.New(message)
}

func newCodexAssistantWaitingRenderer(stderr io.Writer) *codexAssistantWaitingRenderer {
	renderer := &codexAssistantWaitingRenderer{
		stderr:     stderr,
		isTerminal: writerIsTerminal(stderr),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	if renderer.isTerminal {
		go renderer.runSpinner()
		return renderer
	}
	_, _ = fmt.Fprintln(stderr, "assistant> waiting for response...")
	close(renderer.done)
	return renderer
}

func (r *codexAssistantWaitingRenderer) ShowPrompt() {
	if r == nil {
		return
	}
	r.stopWaiting()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptShown {
		return
	}
	_, _ = fmt.Fprint(r.stderr, "assistant> ")
	r.promptShown = true
}

func (r *codexAssistantWaitingRenderer) Finish() {
	if r == nil {
		return
	}
	r.stopWaiting()
}

func (r *codexAssistantWaitingRenderer) stopWaiting() {
	if r == nil {
		return
	}
	select {
	case <-r.done:
		return
	default:
	}
	close(r.stop)
	<-r.done
}

func (r *codexAssistantWaitingRenderer) runSpinner() {
	defer close(r.done)
	frames := []string{"|", "/", "-", "\\"}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()

	frame := 0
	for {
		r.mu.Lock()
		_, _ = fmt.Fprintf(r.stderr, "\rassistant> waiting for response... %s", frames[frame])
		r.mu.Unlock()
		frame = (frame + 1) % len(frames)

		select {
		case <-r.stop:
			r.mu.Lock()
			_, _ = fmt.Fprint(r.stderr, "\r\033[2K")
			r.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}

func codexAssistantCompletedMessage(event codexappserver.Event) string {
	var payload struct {
		Params struct {
			Item struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		} `json:"params"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return strings.TrimSpace(event.Delta)
	}
	if payload.Params.Item.Type != "agentMessage" {
		return ""
	}
	if text := strings.TrimSpace(payload.Params.Item.Text); text != "" {
		return text
	}
	parts := make([]string, 0, len(payload.Params.Item.Content))
	for _, part := range payload.Params.Item.Content {
		text := strings.TrimSpace(part.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func buildCodexCLIDeveloperInstructions(workingDir string, extra string) string {
	base := strings.TrimSpace(`
You are running inside tunnel-client's CLI assistant surface.
Treat the request as being about tunnel-client unless the user explicitly asks about a different package or tool.
Default to the tunnel-client workspace instead of the wider source tree; avoid broad repository scans unless they are clearly necessary.
Prefer direct tunnel-client CLI actions, embedded help topics, and files under the tunnel-client workspace over generic repo exploration.
Keep streamed progress updates concise because the terminal renders incremental deltas directly.
Do not refer to browser-only controls or UI tabs unless the user explicitly asks about the admin UI.
`)
	if workingDir != "" {
		base += "\nStay focused on this workspace root: " + workingDir
	}
	if extra == "" {
		return base
	}
	if strings.Contains(extra, base) {
		return extra
	}
	return base + "\n\n" + extra
}

func buildCodexCLITextInput(prompt string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": prompt,
	}
}

func assistantWorkingDirectory(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		return raw
	}
	if bazelCWD := strings.TrimSpace(os.Getenv("BUILD_WORKING_DIRECTORY")); bazelCWD != "" {
		return inferTunnelClientWorkspace(bazelCWD)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

func inferTunnelClientWorkspace(start string) string {
	start = strings.TrimSpace(start)
	if start == "" {
		return ""
	}
	start = filepath.Clean(start)
	for dir := start; ; dir = filepath.Dir(dir) {
		if looksLikeTunnelClientWorkspace(dir) {
			return dir
		}
		nested := filepath.Join(dir, "api", "tunnel-client")
		if looksLikeTunnelClientWorkspace(nested) {
			return nested
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return start
}

func looksLikeTunnelClientWorkspace(dir string) bool {
	if dir == "" {
		return false
	}
	if !pathExists(filepath.Join(dir, "go.mod")) {
		return false
	}
	return pathExists(filepath.Join(dir, "cmd", "client"))
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func stdinIsTerminal(reader io.Reader) bool {
	return readerIsTerminal(reader)
}

func writerIsTerminal(writer io.Writer) bool {
	return readerIsTerminal(writer)
}

func readerIsTerminal(target any) bool {
	file, ok := target.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func readCodexAssistantPrompt(reader io.Reader) (string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read assistant prompt: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

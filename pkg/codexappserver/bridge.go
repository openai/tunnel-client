package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/fx"
)

const (
	defaultEventCapacity  = 512
	defaultRequestTimeout = 2 * time.Minute
	defaultStderrCapacity = 32
)

type InitializeInfo struct {
	UserAgent      string `json:"user_agent,omitempty"`
	CodexHome      string `json:"codex_home,omitempty"`
	PlatformFamily string `json:"platform_family,omitempty"`
	PlatformOS     string `json:"platform_os,omitempty"`
}

type Account struct {
	Type     string `json:"type,omitempty"`
	Email    string `json:"email,omitempty"`
	PlanType string `json:"plan_type,omitempty"`
}

type LoginState struct {
	Pending         bool      `json:"pending"`
	Type            string    `json:"type,omitempty"`
	LoginID         string    `json:"login_id,omitempty"`
	VerificationURL string    `json:"verification_url,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type ThreadState struct {
	ID             string    `json:"id,omitempty"`
	Preview        string    `json:"preview,omitempty"`
	CWD            string    `json:"cwd,omitempty"`
	Model          string    `json:"model,omitempty"`
	ModelProvider  string    `json:"model_provider,omitempty"`
	ApprovalPolicy string    `json:"approval_policy,omitempty"`
	Sandbox        string    `json:"sandbox,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type TurnState struct {
	ID        string    `json:"id,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	Status    string    `json:"status,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Snapshot struct {
	Command            string         `json:"command"`
	CommandArgs        []string       `json:"command_args,omitempty"`
	CommandCWD         string         `json:"command_cwd,omitempty"`
	PID                int            `json:"pid,omitempty"`
	Running            bool           `json:"running"`
	Starting           bool           `json:"starting"`
	Ready              bool           `json:"ready"`
	Initialized        bool           `json:"initialized"`
	LastError          string         `json:"last_error,omitempty"`
	StartedAt          time.Time      `json:"started_at,omitempty"`
	LastExitAt         time.Time      `json:"last_exit_at,omitempty"`
	InitializeInfo     InitializeInfo `json:"initialize_info,omitempty"`
	AuthMethod         string         `json:"auth_method,omitempty"`
	RequiresOpenAIAuth *bool          `json:"requires_openai_auth,omitempty"`
	Account            *Account       `json:"account,omitempty"`
	Login              *LoginState    `json:"login,omitempty"`
	Thread             *ThreadState   `json:"thread,omitempty"`
	Turn               *TurnState     `json:"turn,omitempty"`
}

type Event struct {
	Seq      int64           `json:"seq"`
	Time     time.Time       `json:"time"`
	Source   string          `json:"source,omitempty"`
	Method   string          `json:"method,omitempty"`
	ThreadID string          `json:"thread_id,omitempty"`
	TurnID   string          `json:"turn_id,omitempty"`
	ItemID   string          `json:"item_id,omitempty"`
	Summary  string          `json:"summary,omitempty"`
	Delta    string          `json:"delta,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

type DeviceCodeLoginResult struct {
	LoginID         string `json:"login_id"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
}

type ThreadStartParams struct {
	CWD                   string
	Model                 string
	ModelProvider         string
	ApprovalPolicy        string
	SandboxType           string
	DeveloperInstructions string
}

type ThreadStartResult struct {
	ThreadID       string `json:"thread_id"`
	CWD            string `json:"cwd,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelProvider  string `json:"model_provider,omitempty"`
	ApprovalPolicy string `json:"approval_policy,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	ThreadPreview  string `json:"thread_preview,omitempty"`
}

type TurnStartParams struct {
	ThreadID       string
	Input          []map[string]any
	CWD            string
	ApprovalPolicy string
	SandboxType    string
	Model          string
	Effort         string
	Summary        string
}

type TurnStartResult struct {
	TurnID   string `json:"turn_id"`
	ThreadID string `json:"thread_id"`
	Status   string `json:"status,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type rpcEnvelope struct {
	ID     any             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type pendingRequest struct {
	ch chan rpcEnvelope
}

type commandConfig struct {
	command string
	args    []string
	cwd     string
}

type Bridge struct {
	logger *slog.Logger

	mu sync.RWMutex

	cfg           commandConfig
	cmd           *exec.Cmd
	pid           int
	stdin         io.WriteCloser
	waitDone      chan struct{}
	running       bool
	starting      bool
	ready         bool
	startupCh     chan struct{}
	shuttingDown  bool
	lastError     string
	startedAt     time.Time
	lastExitAt    time.Time
	initialize    InitializeInfo
	authMethod    string
	requiresAuth  *bool
	account       *Account
	login         *LoginState
	thread        *ThreadState
	turn          *TurnState
	eventHistory  []Event
	eventNext     int
	eventCount    int
	subscribers   map[chan Event]struct{}
	pending       map[int64]pendingRequest
	requestSeq    atomic.Int64
	eventSeq      atomic.Int64
	processStderr []string
}

func NewBridge(lifecycle fx.Lifecycle, logger *slog.Logger) *Bridge {
	cfg := defaultCommandConfig()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := &Bridge{
		logger:       logger.With(slog.String("component", "codexappserver")),
		cfg:          cfg,
		eventHistory: make([]Event, defaultEventCapacity),
		subscribers:  make(map[chan Event]struct{}),
		pending:      make(map[int64]pendingRequest),
	}
	if lifecycle != nil {
		lifecycle.Append(fx.Hook{
			OnStart: func(context.Context) error {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					_ = b.EnsureStarted(ctx)
				}()
				return nil
			},
			OnStop: func(ctx context.Context) error {
				return b.Stop(ctx)
			},
		})
	}
	return b
}

func defaultCommandConfig() commandConfig {
	cwd, _ := os.Getwd()
	if envCWD := strings.TrimSpace(os.Getenv("TUNNEL_CLIENT_CODEX_APP_SERVER_CWD")); envCWD != "" {
		cwd = envCWD
	}
	if envCommand := strings.TrimSpace(os.Getenv("TUNNEL_CLIENT_CODEX_APP_SERVER_COMMAND")); envCommand != "" {
		return commandConfig{
			command: "zsh",
			args:    []string{"-lc", envCommand},
			cwd:     cwd,
		}
	}
	cmdName := strings.TrimSpace(os.Getenv("TUNNEL_CLIENT_CODEX_APP_SERVER_CMD"))
	if cmdName == "" {
		cmdName = "codex"
	}
	args := strings.Fields(strings.TrimSpace(os.Getenv("TUNNEL_CLIENT_CODEX_APP_SERVER_ARGS")))
	if len(args) == 0 {
		args = []string{"app-server"}
	}
	if _, err := exec.LookPath(cmdName); err == nil {
		return commandConfig{
			command: cmdName,
			args:    args,
			cwd:     cwd,
		}
	}
	quoted := append([]string{strconv.Quote(cmdName)}, quoteAll(args)...)
	return commandConfig{
		command: "zsh",
		args:    []string{"-lc", strings.Join(quoted, " ")},
		cwd:     cwd,
	}
}

func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Quote(value))
	}
	return out
}

func (b *Bridge) Warmup() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = b.EnsureStarted(ctx)
	}()
}

func (b *Bridge) EnsureStarted(ctx context.Context) error {
	for {
		b.mu.Lock()
		if b.ready {
			b.mu.Unlock()
			return nil
		}
		if b.starting {
			ch := b.startupCh
			b.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ch:
			}
			continue
		}
		ch := make(chan struct{})
		b.starting = true
		b.startupCh = ch
		b.mu.Unlock()

		go b.startProcess(ch)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (b *Bridge) Stop(ctx context.Context) error {
	b.mu.Lock()
	b.shuttingDown = true
	cmd := b.cmd
	waitDone := b.waitDone
	stdin := b.stdin
	b.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if waitDone == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitDone:
		return nil
	}
}

func (b *Bridge) Snapshot() Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := Snapshot{
		Command:     b.cfg.command,
		CommandArgs: append([]string(nil), b.cfg.args...),
		CommandCWD:  b.cfg.cwd,
		PID:         b.pid,
		Running:     b.running,
		Starting:    b.starting,
		Ready:       b.ready,
		Initialized: b.ready,
		LastError:   b.lastError,
		StartedAt:   b.startedAt,
		LastExitAt:  b.lastExitAt,
		InitializeInfo: InitializeInfo{
			UserAgent:      b.initialize.UserAgent,
			CodexHome:      b.initialize.CodexHome,
			PlatformFamily: b.initialize.PlatformFamily,
			PlatformOS:     b.initialize.PlatformOS,
		},
		AuthMethod: b.authMethod,
	}
	if b.requiresAuth != nil {
		value := *b.requiresAuth
		out.RequiresOpenAIAuth = &value
	}
	if b.account != nil {
		copy := *b.account
		out.Account = &copy
	}
	if b.login != nil {
		copy := *b.login
		out.Login = &copy
	}
	if b.thread != nil {
		copy := *b.thread
		out.Thread = &copy
	}
	if b.turn != nil {
		copy := *b.turn
		out.Turn = &copy
	}
	return out
}

func (b *Bridge) RecentEvents(limit int) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > b.eventCount {
		limit = b.eventCount
	}
	out := make([]Event, 0, limit)
	start := b.eventCount - limit
	for i := start; i < b.eventCount; i++ {
		idx := (b.eventNext - b.eventCount + i + len(b.eventHistory)) % len(b.eventHistory)
		out = append(out, b.eventHistory[idx])
	}
	return out
}

func (b *Bridge) RecentStderr(limit int) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > len(b.processStderr) {
		limit = len(b.processStderr)
	}
	if limit == 0 {
		return nil
	}
	start := len(b.processStderr) - limit
	out := make([]string, 0, limit)
	out = append(out, b.processStderr[start:]...)
	return out
}

func (b *Bridge) RecentDiagnosticSummary(limit int) string {
	events := b.RecentEvents(limit)
	stderr := b.RecentStderr(limit)

	parts := make([]string, 0, 2)
	if len(stderr) > 0 {
		parts = append(parts, "recent stderr: "+strings.Join(stderr, " | "))
	}
	if len(events) > 0 {
		summaries := make([]string, 0, len(events))
		for _, event := range events {
			if event.Source == "stderr" {
				continue
			}
			label := strings.TrimSpace(event.Method)
			summary := strings.TrimSpace(event.Summary)
			switch {
			case label == "":
				label = summary
			case summary != "" && summary != label:
				label += " " + summary
			}
			if label != "" {
				summaries = append(summaries, label)
			}
		}
		if len(summaries) > 0 {
			parts = append(parts, "recent bridge events: "+strings.Join(summaries, " | "))
		}
	}
	return strings.Join(parts, "; ")
}

func (b *Bridge) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 128)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subscribers, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}

func (b *Bridge) StartDeviceCodeLogin(ctx context.Context) (DeviceCodeLoginResult, error) {
	if err := b.EnsureStarted(ctx); err != nil {
		return DeviceCodeLoginResult{}, err
	}
	result, err := b.request(ctx, "account/login/start", map[string]any{
		"type": "chatgptDeviceCode",
	})
	if err != nil {
		return DeviceCodeLoginResult{}, err
	}

	type loginResponse struct {
		Type            string `json:"type"`
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	var response loginResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return DeviceCodeLoginResult{}, fmt.Errorf("decode device-code login response: %w", err)
	}
	if response.Type != "chatgptDeviceCode" {
		return DeviceCodeLoginResult{}, fmt.Errorf("unexpected login response type %q", response.Type)
	}
	login := &LoginState{
		Pending:         true,
		Type:            response.Type,
		LoginID:         response.LoginID,
		VerificationURL: response.VerificationURL,
		UserCode:        response.UserCode,
		StartedAt:       time.Now().UTC(),
	}
	b.mu.Lock()
	b.login = login
	b.mu.Unlock()
	return DeviceCodeLoginResult{
		LoginID:         response.LoginID,
		VerificationURL: response.VerificationURL,
		UserCode:        response.UserCode,
	}, nil
}

func (b *Bridge) CancelLogin(ctx context.Context, loginID string) error {
	if strings.TrimSpace(loginID) == "" {
		b.mu.RLock()
		if b.login != nil {
			loginID = b.login.LoginID
		}
		b.mu.RUnlock()
	}
	if strings.TrimSpace(loginID) == "" {
		return errors.New("login id is required")
	}
	if err := b.EnsureStarted(ctx); err != nil {
		return err
	}
	if _, err := b.request(ctx, "account/login/cancel", map[string]any{"loginId": loginID}); err != nil {
		return err
	}
	b.mu.Lock()
	if b.login != nil && b.login.LoginID == loginID {
		b.login.Pending = false
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) StartThread(ctx context.Context, params ThreadStartParams) (ThreadStartResult, error) {
	if err := b.EnsureStarted(ctx); err != nil {
		return ThreadStartResult{}, err
	}
	stageTimeout := boundedTimeout(ctx, defaultRequestTimeout)
	stageCtx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()
	payload := map[string]any{
		"experimentalRawEvents": false,
	}
	if params.CWD != "" {
		payload["cwd"] = params.CWD
	}
	if params.Model != "" {
		payload["model"] = params.Model
	}
	if params.ModelProvider != "" {
		payload["modelProvider"] = params.ModelProvider
	}
	if params.ApprovalPolicy != "" {
		payload["approvalPolicy"] = params.ApprovalPolicy
	}
	if params.SandboxType != "" {
		payload["sandbox"] = normalizeSandboxType(params.SandboxType)
	}
	if params.DeveloperInstructions != "" {
		payload["developerInstructions"] = params.DeveloperInstructions
	}
	payload["persistExtendedHistory"] = false
	payload["ephemeral"] = true
	result, err := b.request(stageCtx, "thread/start", payload)
	if err != nil {
		return ThreadStartResult{}, b.stageRequestError("thread/start", stageTimeout, err)
	}

	var response struct {
		Thread struct {
			ID        string `json:"id"`
			Preview   string `json:"preview"`
			CWD       string `json:"cwd"`
			CreatedAt int64  `json:"createdAt"`
			UpdatedAt int64  `json:"updatedAt"`
		} `json:"thread"`
		Model          string          `json:"model"`
		ModelProvider  string          `json:"modelProvider"`
		ApprovalPolicy string          `json:"approvalPolicy"`
		Sandbox        json.RawMessage `json:"sandbox"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return ThreadStartResult{}, fmt.Errorf("decode thread/start response: %w", err)
	}
	if response.Thread.ID == "" {
		return ThreadStartResult{}, errors.New("thread/start response did not include thread.id")
	}

	thread := &ThreadState{
		ID:             response.Thread.ID,
		Preview:        response.Thread.Preview,
		CWD:            response.Thread.CWD,
		Model:          response.Model,
		ModelProvider:  response.ModelProvider,
		ApprovalPolicy: response.ApprovalPolicy,
		Sandbox:        decodeSandboxType(response.Sandbox),
		CreatedAt:      unixSeconds(response.Thread.CreatedAt),
		UpdatedAt:      unixSeconds(response.Thread.UpdatedAt),
	}
	b.mu.Lock()
	b.thread = thread
	b.turn = nil
	b.mu.Unlock()

	return ThreadStartResult{
		ThreadID:       thread.ID,
		CWD:            thread.CWD,
		Model:          thread.Model,
		ModelProvider:  thread.ModelProvider,
		ApprovalPolicy: thread.ApprovalPolicy,
		Sandbox:        thread.Sandbox,
		ThreadPreview:  thread.Preview,
	}, nil
}

func (b *Bridge) StartTurn(ctx context.Context, params TurnStartParams) (TurnStartResult, error) {
	if err := b.EnsureStarted(ctx); err != nil {
		return TurnStartResult{}, err
	}
	if strings.TrimSpace(params.ThreadID) == "" {
		b.mu.RLock()
		if b.thread != nil {
			params.ThreadID = b.thread.ID
		}
		b.mu.RUnlock()
	}
	if strings.TrimSpace(params.ThreadID) == "" {
		return TurnStartResult{}, errors.New("thread id is required")
	}
	stageTimeout := boundedTimeout(ctx, defaultRequestTimeout)
	stageCtx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()
	payload := map[string]any{
		"threadId": params.ThreadID,
		"input":    params.Input,
	}
	if params.CWD != "" {
		payload["cwd"] = params.CWD
	}
	if params.ApprovalPolicy != "" {
		payload["approvalPolicy"] = params.ApprovalPolicy
	}
	if params.SandboxType != "" {
		payload["sandboxPolicy"] = map[string]any{"type": normalizeTurnSandboxPolicyType(params.SandboxType)}
	}
	if params.Model != "" {
		payload["model"] = params.Model
	}
	if params.Effort != "" {
		payload["effort"] = params.Effort
	}
	if params.Summary != "" {
		payload["summary"] = params.Summary
	}
	result, err := b.request(stageCtx, "turn/start", payload)
	if err != nil {
		return TurnStartResult{}, b.stageRequestError("turn/start", stageTimeout, err)
	}

	var response struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return TurnStartResult{}, fmt.Errorf("decode turn/start response: %w", err)
	}
	if response.Turn.ID == "" {
		return TurnStartResult{}, errors.New("turn/start response did not include turn.id")
	}
	turn := &TurnState{
		ID:        response.Turn.ID,
		ThreadID:  params.ThreadID,
		Status:    response.Turn.Status,
		UpdatedAt: time.Now().UTC(),
	}
	b.mu.Lock()
	if b.turn != nil && b.turn.ID == turn.ID {
		if turn.ThreadID == "" {
			turn.ThreadID = b.turn.ThreadID
		}
		if isTerminalTurnStatus(b.turn.Status) {
			turn.Status = b.turn.Status
			turn.Error = b.turn.Error
			turn.UpdatedAt = b.turn.UpdatedAt
		}
	}
	b.turn = turn
	b.mu.Unlock()
	return TurnStartResult{
		TurnID:   turn.ID,
		ThreadID: turn.ThreadID,
		Status:   turn.Status,
	}, nil
}

func (b *Bridge) InjectThreadItems(ctx context.Context, threadID string, items []map[string]any) error {
	if err := b.EnsureStarted(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(threadID) == "" {
		return errors.New("thread id is required")
	}
	if len(items) == 0 {
		return nil
	}
	_, err := b.request(ctx, "thread/inject_items", map[string]any{
		"threadId": threadID,
		"items":    items,
	})
	return err
}

func (b *Bridge) startProcess(done chan struct{}) {
	defer close(done)

	if err := b.startProcessLocked(); err != nil {
		b.mu.Lock()
		b.starting = false
		b.ready = false
		b.lastError = err.Error()
		b.mu.Unlock()
		b.publish(Event{
			Time:    time.Now().UTC(),
			Source:  "lifecycle",
			Method:  "process/error",
			Summary: err.Error(),
		})
		return
	}

	b.mu.Lock()
	b.starting = false
	b.ready = true
	b.lastError = ""
	b.mu.Unlock()
}

func (b *Bridge) startProcessLocked() error {
	cmd := exec.Command(b.cfg.command, b.cfg.args...)
	cmd.Dir = b.cfg.cwd
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("start process: %w", err)
	}

	waitDone := make(chan struct{})
	b.mu.Lock()
	b.cmd = cmd
	b.pid = cmd.Process.Pid
	b.stdin = stdin
	b.waitDone = waitDone
	b.running = true
	b.startedAt = time.Now().UTC()
	b.mu.Unlock()

	go b.readStdout(stdout)
	go b.readStderr(stderr)
	go b.waitForExit(cmd, waitDone)

	if err := b.initializeProcess(context.Background()); err != nil {
		return err
	}
	b.publish(Event{
		Time:    time.Now().UTC(),
		Source:  "lifecycle",
		Method:  "process/ready",
		Summary: "codex app-server ready",
	})
	return nil
}

func (b *Bridge) initializeProcess(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	result, err := b.requestNoEnsure(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "tunnel-client-adminui",
			"title":   "tunnel-client admin UI",
			"version": "0.1.0",
		},
		"capabilities": nil,
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var response InitializeInfo
	if err := json.Unmarshal(result, &response); err != nil {
		return fmt.Errorf("decode initialize response: %w", err)
	}
	if err := b.notify(initCtx, "initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}
	b.mu.Lock()
	b.initialize = response
	b.mu.Unlock()
	return b.refreshAccountState(initCtx)
}

func (b *Bridge) refreshAccountState(ctx context.Context) error {
	authResult, err := b.requestNoEnsure(ctx, "getAuthStatus", map[string]any{
		"includeToken": false,
		"refreshToken": false,
	})
	if err == nil {
		var auth struct {
			AuthMethod         string `json:"authMethod"`
			RequiresOpenAIAuth *bool  `json:"requiresOpenaiAuth"`
		}
		if decodeErr := json.Unmarshal(authResult, &auth); decodeErr == nil {
			b.mu.Lock()
			b.authMethod = auth.AuthMethod
			b.requiresAuth = auth.RequiresOpenAIAuth
			b.mu.Unlock()
		}
	}

	accountResult, err := b.requestNoEnsure(ctx, "account/read", map[string]any{
		"refreshToken": false,
	})
	if err != nil {
		return err
	}
	var response struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
		RequiresOpenAIAuth *bool `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(accountResult, &response); err != nil {
		return fmt.Errorf("decode account/read response: %w", err)
	}
	var account *Account
	if response.Account != nil {
		account = &Account{
			Type:     response.Account.Type,
			Email:    response.Account.Email,
			PlanType: response.Account.PlanType,
		}
	}
	b.mu.Lock()
	b.account = account
	if response.RequiresOpenAIAuth != nil {
		b.requiresAuth = response.RequiresOpenAIAuth
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if err := b.EnsureStarted(ctx); err != nil {
		return nil, err
	}
	return b.requestNoEnsure(ctx, method, params)
}

func (b *Bridge) requestNoEnsure(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	payloadID := b.requestSeq.Add(1)
	envelope := map[string]any{
		"id":     payloadID,
		"method": method,
	}
	if params != nil {
		envelope["params"] = params
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	respCh := make(chan rpcEnvelope, 1)

	b.mu.Lock()
	if b.stdin == nil {
		b.mu.Unlock()
		return nil, errors.New("codex app-server stdin unavailable")
	}
	b.pending[payloadID] = pendingRequest{ch: respCh}
	_, err = b.stdin.Write(append(data, '\n'))
	b.mu.Unlock()
	if err != nil {
		b.mu.Lock()
		delete(b.pending, payloadID)
		b.mu.Unlock()
		return nil, fmt.Errorf("write request: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	select {
	case <-requestCtx.Done():
		b.mu.Lock()
		delete(b.pending, payloadID)
		b.mu.Unlock()
		return nil, requestCtx.Err()
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, strings.TrimSpace(resp.Error.Message))
		}
		return resp.Result, nil
	}
}

func (b *Bridge) notify(ctx context.Context, method string, params map[string]any) error {
	payload := map[string]any{
		"method": method,
	}
	if params != nil {
		payload["params"] = params
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b.mu.RLock()
	stdin := b.stdin
	b.mu.RUnlock()
	if stdin == nil {
		return errors.New("codex app-server stdin unavailable")
	}
	done := make(chan error, 1)
	go func() {
		_, err := stdin.Write(append(data, '\n'))
		done <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func isTerminalTurnStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func (b *Bridge) readStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			b.publish(Event{
				Time:    time.Now().UTC(),
				Source:  "stdout",
				Method:  "decode/error",
				Summary: line,
				Payload: json.RawMessage(strconv.Quote(line)),
			})
			continue
		}
		if id, ok := parseRequestID(envelope.ID); ok {
			b.mu.Lock()
			pending, found := b.pending[id]
			if found {
				delete(b.pending, id)
				b.mu.Unlock()
				pending.ch <- envelope
				continue
			}
			b.mu.Unlock()
		}
		b.handleEnvelope(envelope, []byte(line))
	}
	if err := scanner.Err(); err != nil {
		b.mu.Lock()
		b.lastError = fmt.Sprintf("stdout read failed: %v", err)
		b.mu.Unlock()
	}
}

func (b *Bridge) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		b.appendStderrLine(line)
		b.publish(Event{
			Time:    time.Now().UTC(),
			Source:  "stderr",
			Method:  "stderr",
			Summary: line,
			Payload: json.RawMessage(strconv.Quote(line)),
		})
	}
}

func (b *Bridge) appendStderrLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	b.mu.Lock()
	b.processStderr = append(b.processStderr, line)
	if len(b.processStderr) > defaultStderrCapacity {
		b.processStderr = append([]string(nil), b.processStderr[len(b.processStderr)-defaultStderrCapacity:]...)
	}
	b.mu.Unlock()
}

func boundedTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if ctx == nil {
		return fallback
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	remaining := time.Until(deadline)
	switch {
	case remaining <= 0:
		return 0
	case remaining < fallback:
		return remaining
	default:
		return fallback
	}
}

func (b *Bridge) stageRequestError(stage string, timeout time.Duration, err error) error {
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(err.Error())
	if trimmedDetail, ok := strings.CutPrefix(detail, stage+":"); ok {
		detail = strings.TrimSpace(trimmedDetail)
	}
	message := stage + " failed"
	if errors.Is(err, context.DeadlineExceeded) {
		message = fmt.Sprintf("%s timed out after %s", stage, timeout.Round(time.Millisecond))
		detail = ""
	}
	if detail != "" && detail != context.DeadlineExceeded.Error() {
		message += ": " + detail
	}
	if diagnostics := b.RecentDiagnosticSummary(6); diagnostics != "" {
		message += "; " + diagnostics
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", message, err)
	}
	return errors.New(message)
}

func (b *Bridge) waitForExit(cmd *exec.Cmd, done chan struct{}) {
	defer close(done)
	err := cmd.Wait()

	b.mu.Lock()
	for id, pending := range b.pending {
		delete(b.pending, id)
		pending.ch <- rpcEnvelope{Error: &rpcError{Message: "codex app-server exited"}}
	}
	b.ready = false
	b.starting = false
	b.stdin = nil
	b.cmd = nil
	b.pid = 0
	b.running = false
	b.lastExitAt = time.Now().UTC()
	if err != nil && !b.shuttingDown {
		b.lastError = err.Error()
	}
	b.mu.Unlock()

	summary := "codex app-server exited"
	if err != nil {
		summary = err.Error()
	}
	b.publish(Event{
		Time:    time.Now().UTC(),
		Source:  "lifecycle",
		Method:  "process/exited",
		Summary: summary,
	})
}

func (b *Bridge) handleEnvelope(envelope rpcEnvelope, raw json.RawMessage) {
	event := Event{
		Time:    time.Now().UTC(),
		Source:  "notification",
		Method:  envelope.Method,
		Payload: append(json.RawMessage(nil), raw...),
	}

	var params map[string]any
	if len(envelope.Params) > 0 {
		_ = json.Unmarshal(envelope.Params, &params)
		event.ThreadID = stringValue(params["threadId"])
		event.TurnID = stringValue(params["turnId"])
		event.ItemID = stringValue(params["itemId"])
	}

	switch envelope.Method {
	case "mcpServer/startupStatus/updated", "account/rateLimits/updated":
		return
	case "account/updated":
		if authMode := stringValue(params["authMode"]); authMode != "" {
			b.mu.Lock()
			b.authMethod = authMode
			b.mu.Unlock()
			event.Summary = "account auth mode updated"
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = b.refreshAccountState(ctx)
		}()
	case "account/login/completed":
		success, _ := params["success"].(bool)
		loginID := stringValue(params["loginId"])
		errText := stringValue(params["error"])
		b.mu.Lock()
		if b.login != nil && (loginID == "" || b.login.LoginID == loginID) {
			b.login.Pending = false
			b.login.LastError = errText
		}
		b.mu.Unlock()
		if success {
			event.Summary = "device-code login completed"
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = b.refreshAccountState(ctx)
			}()
		} else {
			event.Summary = "device-code login failed"
		}
	case "thread/started":
		var payload struct {
			Thread struct {
				ID        string `json:"id"`
				Preview   string `json:"preview"`
				CWD       string `json:"cwd"`
				CreatedAt int64  `json:"createdAt"`
				UpdatedAt int64  `json:"updatedAt"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(envelope.Params, &payload); err == nil && payload.Thread.ID != "" {
			b.mu.Lock()
			b.thread = &ThreadState{
				ID:        payload.Thread.ID,
				Preview:   payload.Thread.Preview,
				CWD:       payload.Thread.CWD,
				CreatedAt: unixSeconds(payload.Thread.CreatedAt),
				UpdatedAt: unixSeconds(payload.Thread.UpdatedAt),
			}
			b.turn = nil
			b.mu.Unlock()
			event.ThreadID = payload.Thread.ID
			event.Summary = "thread started"
		}
	case "turn/started":
		var payload struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(envelope.Params, &payload); err == nil {
			if payload.Turn.ID == "" {
				payload.Turn.ID = stringValue(params["turnId"])
			}
			if payload.Turn.Status == "" {
				payload.Turn.Status = stringValue(params["status"])
			}
		}
		if payload.Turn.ID != "" {
			threadID := payload.ThreadID
			if threadID == "" {
				threadID = stringValue(params["threadId"])
			}
			b.mu.Lock()
			b.turn = &TurnState{
				ID:        payload.Turn.ID,
				ThreadID:  threadID,
				Status:    payload.Turn.Status,
				UpdatedAt: time.Now().UTC(),
			}
			b.mu.Unlock()
			event.ThreadID = threadID
			event.TurnID = payload.Turn.ID
			event.Summary = "turn started"
		}
	case "turn/completed":
		var payload struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(envelope.Params, &payload); err == nil {
			if payload.Turn.ID == "" {
				payload.Turn.ID = stringValue(params["turnId"])
			}
			if payload.Turn.Status == "" {
				payload.Turn.Status = stringValue(params["status"])
			}
		}
		if payload.Turn.ID != "" {
			threadID := payload.ThreadID
			if threadID == "" {
				threadID = stringValue(params["threadId"])
			}
			turn := &TurnState{
				ID:        payload.Turn.ID,
				ThreadID:  threadID,
				Status:    payload.Turn.Status,
				UpdatedAt: time.Now().UTC(),
			}
			if payload.Turn.Error != nil {
				turn.Error = payload.Turn.Error.Message
			}
			b.mu.Lock()
			b.turn = turn
			b.mu.Unlock()
			event.ThreadID = threadID
			event.TurnID = payload.Turn.ID
			event.Summary = "turn completed"
		}
	case "item/agentMessage/delta":
		event.Delta = stringValue(params["delta"])
		event.Summary = "assistant delta"
	case "item/commandExecution/outputDelta":
		event.Delta = stringValue(params["delta"])
		event.Summary = "command output delta"
	case "item/completed":
		var payload struct {
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(envelope.Params, &payload); err == nil {
			event.Summary = payload.Item.Type + " completed"
			if payload.Item.Type == "agentMessage" && event.Delta == "" {
				event.Delta = payload.Item.Text
			}
		}
	case "error":
		if errInfo, ok := params["error"].(map[string]any); ok {
			event.Summary = stringValue(errInfo["message"])
		}
		if event.Summary == "" {
			event.Summary = "turn error"
		}
	default:
		if event.Summary == "" {
			event.Summary = envelope.Method
		}
	}

	b.publish(event)
}

func (b *Bridge) publish(event Event) {
	event.Seq = b.eventSeq.Add(1)
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}

	b.mu.Lock()
	if len(b.eventHistory) > 0 {
		b.eventHistory[b.eventNext] = event
		b.eventNext = (b.eventNext + 1) % len(b.eventHistory)
		if b.eventCount < len(b.eventHistory) {
			b.eventCount++
		}
	}
	subs := make([]chan Event, 0, len(b.subscribers))
	for sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub <- event:
		default:
		}
	}
}

func parseRequestID(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func unixSeconds(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func normalizeSandboxType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "dangerFullAccess":
		return "danger-full-access"
	case "workspaceWrite":
		return "workspace-write"
	case "readOnly":
		return "read-only"
	default:
		return strings.TrimSpace(raw)
	}
}

func decodeSandboxType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var sandbox string
	if err := json.Unmarshal(raw, &sandbox); err == nil {
		return normalizeSandboxType(sandbox)
	}

	var typed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &typed); err == nil {
		return normalizeSandboxType(typed.Type)
	}

	return ""
}

func normalizeTurnSandboxPolicyType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "danger-full-access":
		return "dangerFullAccess"
	case "workspace-write":
		return "workspaceWrite"
	case "read-only":
		return "readOnly"
	default:
		return strings.TrimSpace(raw)
	}
}

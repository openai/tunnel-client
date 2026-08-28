package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// NewSerializedForwardingTransport wraps a ForwardingTransport so only one
// request lifecycle is active on the shared underlying connection at a time.
//
// Some MCP transports multiplex poorly when several connector calls write to the
// same long-lived connection and then each reader waits for its own response. The
// wrapper holds a lifecycle slot from Connect through any streamed notifications
// until the matching final JSON-RPC response, an error, or Close. Notifications
// without ids release immediately after the write because no response is legal.
func NewSerializedForwardingTransport(base ForwardingTransport) ForwardingTransport {
	return newSerializedForwardingTransport(base, false, false, false)
}

// NewSerializedForwardingTransportWithDeadlineRetirement wraps a shared
// transport whose physical connection must survive one request deadline.
//
// A deadline-retired request releases the lifecycle slot after recording its
// response ID. Later readers discard that retired response before it can be
// mistaken for a newer request. While any retired response remains outstanding,
// server requests and notifications are dropped because the JSON-RPC wire
// format does not identify which in-flight request owns them.
//
// Stdio uses this because all logical connections share one child-process
// stdin/stdout pair. Closing those pipes for one deadline would poison every
// later request. Admitting the next request after retirement keeps terminal
// responses flowing even when the timed-out server work never replies.
func NewSerializedForwardingTransportWithDeadlineRetirement(base ForwardingTransport) ForwardingTransport {
	return newSerializedForwardingTransport(base, true, false, false)
}

// NewStdioDeadlineRetiringForwardingTransport applies shared stdio's
// serialization and deadline-retirement behavior while preserving verbatim
// lifecycle forwarding. If a later logical session reuses an outstanding
// retired response ID, it uses a private downstream alias so the stale response
// can stay tombstoned without wedging future sessions.
func NewStdioDeadlineRetiringForwardingTransport(base ForwardingTransport) ForwardingTransport {
	return newSerializedForwardingTransport(base, true, false, true)
}

// NewStdioForwardingTransport opts a shared stdio transport into the
// serialization and deadline-retirement behavior it needs, plus a compatibility
// shim for callers that omit MCP's initialized notification. After a successful
// initialize response, the wrapper writes notifications/initialized before it
// releases the lifecycle slot. If the caller later forwards the same
// notification, the wrapper acknowledges it without writing a duplicate to the
// stdio server. Callers must choose this wrapper explicitly so legacy stdio
// servers keep verbatim forwarding by default.
func NewStdioForwardingTransport(base ForwardingTransport) ForwardingTransport {
	return newSerializedForwardingTransport(base, true, true, true)
}

func newSerializedForwardingTransport(base ForwardingTransport, retireOnDeadline, ensureInitialized, aliasRetiredResponseIDs bool) ForwardingTransport {
	if base == nil {
		return nil
	}
	return &serializedForwardingTransport{
		base:               base,
		lifecycleSlot:      make(chan struct{}, 1),
		retireOnDeadline:   retireOnDeadline,
		ensureInitialized:  ensureInitialized,
		aliasRetiredIDs:    aliasRetiredResponseIDs,
		retiredResponseIDs: make(map[jsonrpc.ID]struct{}),
	}
}

type serializedForwardingTransport struct {
	base             ForwardingTransport
	lifecycleSlot    chan struct{}
	retireOnDeadline bool
	// ensureInitialized is enabled only for stdio. The tunnel normally forwards
	// the caller's lifecycle messages verbatim, but older callers can omit the
	// required notification and leave a compliant stdio server waiting forever.
	ensureInitialized bool
	// aliasRetiredIDs is stdio-only. Generic deadline-retiring wrappers keep
	// rejecting reused IDs because their opaque preserved-error payloads cannot
	// safely restore a private downstream alias.
	aliasRetiredIDs bool

	retiredMu          sync.Mutex
	retiredResponseIDs map[jsonrpc.ID]struct{}
	nextAliasedID      uint64

	initializedMu           sync.Mutex
	initializedNotification bool
}

const maxRetiredResponseIDs = 1024

const initializedNotificationMethod = "notifications/initialized"

const retiredResponseAliasPrefix = "__tunnel_client_stdio_retired_"

func (t *serializedForwardingTransport) Connect(
	ctx context.Context,
) (ForwardingConnection, error) {
	if err := t.acquireLifecycle(ctx); err != nil {
		return nil, err
	}

	conn, err := t.base.Connect(ctx)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.releaseLifecycle()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		if conn != nil && (!t.retireOnDeadline || !hasResponseDeadlineEnforcement(ctx)) {
			_ = conn.Close()
		}
		t.releaseLifecycle()
		return nil, err
	}
	return &serializedForwardingConnection{
		acquireLifecycle: t.acquireLifecycle,
		base:             conn,
		releaseLifecycle: t.releaseLifecycle,
		transport:        t,
		lockHeld:         true,
	}, nil
}

func (t *serializedForwardingTransport) acquireLifecycle(ctx context.Context) error {
	select {
	case t.lifecycleSlot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			t.releaseLifecycle()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *serializedForwardingTransport) releaseLifecycle() {
	<-t.lifecycleSlot
}

func (t *serializedForwardingTransport) retireResponseID(id jsonrpc.ID) bool {
	if t == nil || !id.IsValid() {
		return false
	}
	t.retiredMu.Lock()
	defer t.retiredMu.Unlock()
	if _, ok := t.retiredResponseIDs[id]; ok {
		return true
	}
	if len(t.retiredResponseIDs) >= maxRetiredResponseIDs {
		return false
	}
	t.retiredResponseIDs[id] = struct{}{}
	return true
}

// wireResponseID keeps a caller ID verbatim unless an earlier timed-out
// request with that ID can still produce a late response on the shared stdio
// stream. In that case, use a fresh downstream-only ID so the old response can
// remain tombstoned without rejecting every later logical session that reuses
// the caller's ID.
func (t *serializedForwardingTransport) wireResponseID(callerID jsonrpc.ID) (jsonrpc.ID, error) {
	if t == nil || !callerID.IsValid() {
		return callerID, nil
	}

	t.retiredMu.Lock()
	defer t.retiredMu.Unlock()
	if _, retired := t.retiredResponseIDs[callerID]; !retired {
		return callerID, nil
	}
	if !t.aliasRetiredIDs {
		return jsonrpc.ID{}, fmt.Errorf("MCP request ID %v is still retired", callerID.Raw())
	}

	for {
		if t.nextAliasedID == ^uint64(0) {
			return jsonrpc.ID{}, errors.New("mcpclient: exhausted stdio response ID aliases")
		}
		t.nextAliasedID++
		aliasID, err := jsonrpc.MakeID(fmt.Sprintf("%s%d", retiredResponseAliasPrefix, t.nextAliasedID))
		if err != nil {
			return jsonrpc.ID{}, fmt.Errorf("mcpclient: build stdio response ID alias: %w", err)
		}
		if aliasID == callerID {
			continue
		}
		if _, retired := t.retiredResponseIDs[aliasID]; retired {
			continue
		}
		return aliasID, nil
	}
}

func (t *serializedForwardingTransport) consumeRetiredResponseID(id jsonrpc.ID) bool {
	if t == nil || !id.IsValid() {
		return false
	}
	t.retiredMu.Lock()
	defer t.retiredMu.Unlock()
	if _, ok := t.retiredResponseIDs[id]; !ok {
		return false
	}
	delete(t.retiredResponseIDs, id)
	return true
}

func (t *serializedForwardingTransport) hasRetiredResponseIDs() bool {
	if t == nil {
		return false
	}
	t.retiredMu.Lock()
	defer t.retiredMu.Unlock()
	return len(t.retiredResponseIDs) > 0
}

func (t *serializedForwardingTransport) shouldDropRetiredMessage(msg jsonrpc.Message) bool {
	if t == nil || msg == nil {
		return false
	}
	if response, ok := msg.(*jsonrpc.Response); ok {
		return response.ID.IsValid() && t.consumeRetiredResponseID(response.ID)
	}
	if _, ok := msg.(*jsonrpc.Request); ok {
		return t.hasRetiredResponseIDs()
	}
	return false
}

type serializedForwardingConnection struct {
	base ForwardingConnection

	transport *serializedForwardingTransport

	acquireLifecycle func(context.Context) error
	releaseLifecycle func()

	stateMu           sync.Mutex
	lockHeld          bool
	writeStarted      bool
	writeCompleted    bool
	awaitingResponse  bool
	callerID          jsonrpc.ID
	expectedID        jsonrpc.ID
	requestMethod     string
	deadlineRetirable bool
	retired           bool
}

func (c *serializedForwardingConnection) Write(
	ctx context.Context,
	header http.Header,
	msg jsonrpc.Message,
) (ForwardingWriteResult, error) {
	if c.base == nil {
		c.release()
		return ForwardingWriteResult{}, nil
	}

	expectResponse, callerID, method := expectedResponse(msg)
	expectedID, err := c.acquire(ctx, expectResponse, callerID, method)
	if err != nil {
		return ForwardingWriteResult{}, err
	}
	if method == "initialize" && c.transport != nil {
		c.transport.resetInitializedNotification()
	}
	if c.shouldSuppressInitializedNotification(msg) {
		c.markWriteCompleted(true)
		c.markDeadlineRetirableWithoutResponse(true)
		c.release()
		return ForwardingWriteResult{}, nil
	}

	result, err := c.base.Write(ctx, header, requestWithResponseID(msg, callerID, expectedID))
	c.markWriteCompleted(err == nil)
	awaitDeadlineRetirement := err != nil && c.shouldAwaitDeadlineRetirement(ctx, err)
	if c.transport != nil && !awaitDeadlineRetirement && (err != nil || result.PreservedError != nil || result.StatusCode >= http.StatusBadRequest) {
		c.transport.resetInitializedNotification()
	}
	if awaitDeadlineRetirement {
		// The processor decides whether this context error is specifically the
		// response-deadline path. Keep the slot until it retires or closes.
	} else if !expectResponse {
		c.markDeadlineRetirableWithoutResponse(err == nil)
		c.release()
	} else if err != nil || result.PreservedError != nil || result.StatusCode >= http.StatusBadRequest {
		c.release()
	}
	return result, err
}

func (c *serializedForwardingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if c.base == nil {
		c.release()
		return nil, nil
	}

	for {
		msg, err := c.base.Read(ctx)
		if err != nil || msg == nil {
			if err != nil && c.shouldAwaitDeadlineRetirement(ctx, err) {
				// The processor owns the response-deadline decision. Keep the
				// slot held until it retires or closes this logical connection.
				return msg, err
			}
			c.release()
			return msg, err
		}
		if c.transport != nil && c.transport.shouldDropRetiredMessage(msg) {
			continue
		}
		if c.shouldEnsureInitializedNotification(msg) {
			if err := c.writeInitializedNotification(ctx); err != nil {
				c.release()
				return nil, err
			}
		}
		callerMsg := c.restoreCallerResponseID(msg)
		if c.shouldReleaseAfterRead(msg) {
			c.release()
		}
		return callerMsg, nil
	}
}

func requestWithResponseID(msg jsonrpc.Message, callerID, expectedID jsonrpc.ID) jsonrpc.Message {
	if !callerID.IsValid() || callerID == expectedID {
		return msg
	}
	request, ok := msg.(*jsonrpc.Request)
	if !ok || request == nil {
		return msg
	}
	cloned := *request
	cloned.ID = expectedID
	return &cloned
}

func (c *serializedForwardingConnection) restoreCallerResponseID(msg jsonrpc.Message) jsonrpc.Message {
	response, ok := msg.(*jsonrpc.Response)
	if !ok || response == nil {
		return msg
	}

	c.stateMu.Lock()
	lockHeld := c.lockHeld
	awaitingResponse := c.awaitingResponse
	callerID := c.callerID
	expectedID := c.expectedID
	c.stateMu.Unlock()
	if !lockHeld || !awaitingResponse || !callerID.IsValid() || callerID == expectedID || response.ID != expectedID {
		return msg
	}

	cloned := *response
	cloned.ID = callerID
	return &cloned
}

func (c *serializedForwardingConnection) shouldSuppressInitializedNotification(msg jsonrpc.Message) bool {
	if c == nil || c.transport == nil || !c.transport.ensureInitialized || !c.transport.initializedNotificationSent() {
		return false
	}
	request, ok := msg.(*jsonrpc.Request)
	return ok && request != nil && !request.ID.IsValid() && request.Method == initializedNotificationMethod
}

func (c *serializedForwardingConnection) shouldEnsureInitializedNotification(msg jsonrpc.Message) bool {
	if c == nil || c.transport == nil || !c.transport.ensureInitialized {
		return false
	}
	response, ok := msg.(*jsonrpc.Response)
	if !ok || response == nil || response.Error != nil || !response.ID.IsValid() {
		return false
	}

	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.lockHeld && c.awaitingResponse && c.requestMethod == "initialize" && c.expectedID.IsValid() && response.ID == c.expectedID
}

func (c *serializedForwardingConnection) writeInitializedNotification(ctx context.Context) error {
	// The initialize response has already been read successfully. Detach the
	// tiny lifecycle write from the tunnel command deadline so cancellation in
	// the handoff gap cannot skip the notification and poison the shared stdio
	// session before the response is delivered upstream.
	writeCtx := context.WithoutCancel(ctx)
	result, err := c.base.Write(writeCtx, nil, &jsonrpc.Request{Method: initializedNotificationMethod})
	if err != nil {
		return fmt.Errorf("send MCP initialized notification after initialize: %w", err)
	}
	if result.PreservedError != nil {
		return fmt.Errorf("send MCP initialized notification after initialize: downstream rejected notification")
	}
	if result.StatusCode != 0 && (result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices) {
		return fmt.Errorf("send MCP initialized notification after initialize: downstream returned status %d", result.StatusCode)
	}
	c.transport.markInitializedNotificationSent()
	return nil
}

func (t *serializedForwardingTransport) initializedNotificationSent() bool {
	if t == nil {
		return false
	}
	t.initializedMu.Lock()
	defer t.initializedMu.Unlock()
	return t.initializedNotification
}

func (t *serializedForwardingTransport) markInitializedNotificationSent() {
	if t == nil {
		return
	}
	t.initializedMu.Lock()
	t.initializedNotification = true
	t.initializedMu.Unlock()
}

func (t *serializedForwardingTransport) resetInitializedNotification() {
	if t == nil || !t.ensureInitialized {
		return
	}
	t.initializedMu.Lock()
	t.initializedNotification = false
	t.initializedMu.Unlock()
}

func (c *serializedForwardingConnection) Close() error {
	if c.base == nil {
		c.release()
		return nil
	}
	if c.isRetired() {
		return nil
	}
	if c.transport != nil {
		c.transport.resetInitializedNotification()
	}
	defer c.release()
	return c.base.Close()
}

// RetireResponseDeadline prevents one response deadline from closing a shared
// physical transport. It returns false for initialize or for transports that
// do not support retirement, so the caller can retain the fail-closed path.
//
// For a known-written request, the response ID is recorded before the lifecycle
// slot is released. A later logical request can then discard the stale response
// without closing the shared stdio pipes. If no request was written, there is
// no late response to track and retirement only releases the logical slot.
func (c *serializedForwardingConnection) RetireResponseDeadline() bool {
	if c == nil || c.transport == nil || !c.transport.retireOnDeadline {
		return false
	}

	expectedID, shouldTrack, shouldRelease, handled := c.beginDeadlineRetirement()
	if !handled {
		return false
	}
	if !shouldRelease {
		return true
	}
	if shouldTrack && !c.transport.retireResponseID(expectedID) {
		c.cancelDeadlineRetirement()
		return false
	}
	c.releaseRetired()
	return true
}

func (c *serializedForwardingConnection) acquire(ctx context.Context, expectResponse bool, callerID jsonrpc.ID, method string) (jsonrpc.ID, error) {
	c.stateMu.Lock()
	reuseHeldSlot := c.lockHeld && !c.writeStarted
	c.stateMu.Unlock()

	if !reuseHeldSlot && c.acquireLifecycle != nil {
		if err := c.acquireLifecycle(ctx); err != nil {
			return jsonrpc.ID{}, err
		}
	}

	expectedID := callerID
	if expectResponse && c.transport != nil {
		var err error
		expectedID, err = c.transport.wireResponseID(callerID)
		if err != nil {
			if reuseHeldSlot {
				c.release()
			} else if c.releaseLifecycle != nil {
				c.releaseLifecycle()
			}
			return jsonrpc.ID{}, err
		}
	}

	c.stateMu.Lock()
	c.lockHeld = true
	c.writeStarted = true
	c.writeCompleted = false
	c.awaitingResponse = expectResponse
	c.callerID = callerID
	c.expectedID = expectedID
	c.requestMethod = method
	c.deadlineRetirable = false
	c.retired = false
	c.stateMu.Unlock()
	return expectedID, nil
}

func (c *serializedForwardingConnection) markWriteCompleted(completed bool) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	if c.lockHeld && c.writeStarted {
		c.writeCompleted = completed
	}
	c.stateMu.Unlock()
}

func (c *serializedForwardingConnection) markDeadlineRetirableWithoutResponse(retirable bool) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	if c.lockHeld && c.writeStarted {
		c.deadlineRetirable = retirable
	}
	c.stateMu.Unlock()
}

func (c *serializedForwardingConnection) shouldAwaitDeadlineRetirement(ctx context.Context, err error) bool {
	if c == nil || c.transport == nil || !c.transport.retireOnDeadline {
		return false
	}
	if !hasResponseDeadlineEnforcement(ctx) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (c *serializedForwardingConnection) shouldReleaseAfterRead(msg jsonrpc.Message) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if !c.lockHeld {
		return false
	}
	if !c.awaitingResponse {
		return true
	}

	response, ok := msg.(*jsonrpc.Response)
	if !ok {
		return false
	}

	if !response.ID.IsValid() || response.ID != c.expectedID {
		c.awaitingResponse = false
		return true
	}

	c.awaitingResponse = false
	return true
}

func (c *serializedForwardingConnection) release() {
	if !c.markReleased() {
		return
	}
	if c.releaseLifecycle != nil {
		c.releaseLifecycle()
	}
}

func (c *serializedForwardingConnection) releaseRetired() {
	c.stateMu.Lock()
	lockHeld := c.lockHeld
	if lockHeld {
		c.clearLifecycleStateLocked()
	}
	c.retired = true
	c.stateMu.Unlock()

	if lockHeld && c.releaseLifecycle != nil {
		c.releaseLifecycle()
	}
}

func (c *serializedForwardingConnection) cancelDeadlineRetirement() {
	c.stateMu.Lock()
	if c.lockHeld {
		c.retired = false
	}
	c.stateMu.Unlock()
}

func (c *serializedForwardingConnection) markReleased() bool {
	c.stateMu.Lock()
	lockHeld := c.lockHeld
	if lockHeld {
		c.clearLifecycleStateLocked()
	}
	c.retired = false
	c.stateMu.Unlock()
	return lockHeld
}

func (c *serializedForwardingConnection) beginDeadlineRetirement() (jsonrpc.ID, bool, bool, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if c.retired {
		return jsonrpc.ID{}, false, false, true
	}
	if !c.lockHeld {
		if c.deadlineRetirable {
			c.deadlineRetirable = false
			c.retired = true
			return jsonrpc.ID{}, false, false, true
		}
		return jsonrpc.ID{}, false, false, false
	}
	if !c.writeCompleted {
		c.retired = true
		return jsonrpc.ID{}, false, true, true
	}
	if c.requestMethod == "initialize" {
		return jsonrpc.ID{}, false, false, false
	}
	c.retired = true
	if c.writeCompleted && c.awaitingResponse && c.expectedID.IsValid() {
		return c.expectedID, true, true, true
	}
	return jsonrpc.ID{}, false, true, true
}

func (c *serializedForwardingConnection) clearLifecycleStateLocked() {
	c.lockHeld = false
	c.writeStarted = false
	c.writeCompleted = false
	c.awaitingResponse = false
	c.callerID = jsonrpc.ID{}
	c.expectedID = jsonrpc.ID{}
	c.requestMethod = ""
}

func (c *serializedForwardingConnection) isRetired() bool {
	if c == nil {
		return false
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.retired
}

func expectedResponse(msg jsonrpc.Message) (bool, jsonrpc.ID, string) {
	request, ok := msg.(*jsonrpc.Request)
	if !ok {
		return false, jsonrpc.ID{}, ""
	}
	return request.ID.IsValid(), request.ID, request.Method
}

package mcpclient

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

const (
	maxObservationResultBytes = 1 << 20
	maxObservationParamsBytes = 8 << 10
	maxObservationCursorBytes = 4 << 10
	maxObservationPages       = 32
	maxObservationToolNames   = 256
	maxObservationToolBytes   = 128
	maxObservationNamesJSON   = 16 << 10
	maxObservationIdentity    = 256
	maxObservationVersions    = 32
)

// ProtocolObservation owns bounded passive discovery evidence for the main
// channel. It is shared by the actual child owner, forwarding path, and health
// provider; reading it never creates a connection or sends a protocol message.
type ProtocolObservation struct {
	mu    sync.Mutex
	now   func() time.Time
	probe *ProbeState

	details                    healthstate.MCPDetails
	state                      string
	reason                     string
	observedAt                 *time.Time
	epochLimited               bool
	serverDiscoverEpochLimited bool

	catalogRevision        uint64
	catalogLimited         bool
	catalogActive          bool
	catalogProtocolVersion string
	pages                  int
	nextCursor             [sha256.Size]byte
	hasNextCursor          bool
	seenCursors            map[[sha256.Size]byte]struct{}
}

type protocolObservationToken struct {
	generation          string
	epoch               uint64
	serverDiscoverEpoch uint64
	revision            uint64
	method              string
	protocolVersion     string
	acceptPage          bool
}

// NewProtocolObservation constructs the passive main-channel health provider.
func NewProtocolObservation(cfg *runtimeconfig.MCPConfig, probe *ProbeState) *ProtocolObservation {
	o := &ProtocolObservation{now: time.Now, probe: probe, state: "not_observed"}
	o.details.Channel = types.DefaultChannel.String()
	o.details.Evidence = "same_child"
	if cfg == nil || cfg.AllowNoMain {
		o.state = "disabled"
		o.details.Evidence = "not_configured"
		return o
	}
	kind := cfg.TransportKind
	if binding := cfg.MainChannelBinding(); binding != nil {
		kind = binding.TransportKind
	}
	if kind == "" {
		kind = runtimeconfig.MCPTransportHTTPStreamable
	}
	o.details.Transport = string(kind)
	if kind == runtimeconfig.MCPTransportStdio {
		o.details.ChildState = "not_started"
	} else {
		o.details.Evidence = "unsupported_transport"
		o.reason = "same_child_evidence_unavailable"
	}
	return o
}

func (o *ProtocolObservation) Name() string { return "mcp" }

func (o *ProtocolObservation) Snapshot(_ time.Time) healthstate.ComponentSnapshot {
	o.mu.Lock()
	details := o.details
	details.Initialize.CapabilityNames = append([]string{}, details.Initialize.CapabilityNames...)
	if details.ServerDiscover != nil {
		discover := *details.ServerDiscover
		discover.SupportedVersions = append([]string{}, discover.SupportedVersions...)
		discover.CapabilityNames = append([]string{}, discover.CapabilityNames...)
		discover.ObservedAt = copyObservationTime(discover.ObservedAt)
		details.ServerDiscover = &discover
	}
	details.ToolsList.ToolNames = append([]string{}, details.ToolsList.ToolNames...)
	details.Initialize.ObservedAt = copyObservationTime(details.Initialize.ObservedAt)
	details.ToolsList.ObservedAt = copyObservationTime(details.ToolsList.ObservedAt)
	snapshot := healthstate.ComponentSnapshot{
		Status: healthstate.StatusUnknown, State: o.state, ReasonCode: o.reason,
		ObservedAt: copyObservationTime(o.observedAt),
		Limited: details.Initialize.Limited || details.ToolsList.Limited || o.epochLimited || o.serverDiscoverEpochLimited ||
			(details.ServerDiscover != nil && details.ServerDiscover.Limited) || o.catalogLimited,
	}
	o.mu.Unlock()
	switch snapshot.State {
	case "disabled":
		snapshot.Status = healthstate.StatusDisabled
	case "initialized", "server_discovered", "discovered":
		snapshot.Status = healthstate.StatusOK
	case "failed", "closed":
		snapshot.Status = healthstate.StatusDegraded
	}
	if details.Transport == string(runtimeconfig.MCPTransportHTTPStreamable) && o.probe != nil {
		probe := &healthstate.MCPStartupProbe{State: "pending"}
		if at, err, done := o.probe.Wait(0); done {
			probe.ObservedAt = &at
			switch {
			case err == nil:
				probe.State = "succeeded"
			case IsAuthRequiredProbeError(err):
				probe.State = "auth_required"
			case IsTimeoutProbeError(err):
				probe.State = "timed_out"
			default:
				probe.State = "failed"
			}
		}
		details.StartupProbe = probe
	}
	snapshot.Details = details
	return snapshot
}

func copyObservationTime(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	copy := *at
	return &copy
}

func (o *ProtocolObservation) beginChild() string {
	if o == nil {
		return ""
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	generation := hex.EncodeToString(id[:])
	o.mu.Lock()
	defer o.mu.Unlock()
	o.details.ChildGeneration = generation
	o.details.ChildState = "starting"
	o.details.InitializeEpoch = 0
	o.details.ServerDiscoverEpoch = 0
	o.details.Initialize = healthstate.MCPInitialize{}
	o.details.ServerDiscover = nil
	o.details.ToolsList = healthstate.MCPToolsList{}
	o.state, o.reason, o.observedAt = "not_observed", "", nil
	o.epochLimited, o.serverDiscoverEpochLimited, o.catalogLimited = false, false, false
	o.catalogRevision = 0
	o.resetCatalogLocked()
	return generation
}

func (o *ProtocolObservation) childStarted(generation string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation == o.details.ChildGeneration && o.details.ChildState == "starting" {
		o.details.ChildState = "running"
	}
}

func (o *ProtocolObservation) childClosed(generation, reason string) {
	if o == nil || generation == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation != o.details.ChildGeneration || o.details.ChildState == "closed" {
		return
	}
	o.details.ChildState = "closed"
	o.details.Initialize = healthstate.MCPInitialize{}
	o.details.ServerDiscover = nil
	o.resetCatalogLocked()
	o.state, o.reason = "closed", reason
	o.observedAt = o.timestampLocked()
}

func (o *ProtocolObservation) generation() string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.details.ChildGeneration
}

func (o *ProtocolObservation) timestampLocked() *time.Time {
	at := o.now().UTC()
	return &at
}

func (o *ProtocolObservation) resetCatalogLocked() {
	o.details.ToolsList = healthstate.MCPToolsList{}
	o.catalogActive = false
	o.catalogProtocolVersion = ""
	o.pages = 0
	o.hasNextCursor = false
	o.nextCursor = [sha256.Size]byte{}
	o.seenCursors = nil
}

func (o *ProtocolObservation) advanceCatalogLocked() bool {
	if o.catalogLimited || o.catalogRevision == math.MaxUint64 {
		o.catalogLimited = true
		o.invalidateCatalogLocked("catalog_revision_limit", true)
		return false
	}
	o.catalogRevision++
	return true
}

// requestWritten runs only after a successful physical write. The token carries
// no caller data and remains valid only for that child and discovery attempt.
func (o *ProtocolObservation) requestWritten(generation string, req *jsonrpc.Request, protocolVersion string) protocolObservationToken {
	if o == nil || req == nil || !req.ID.IsValid() ||
		(req.Method != "initialize" && req.Method != "server/discover" && req.Method != "tools/list") {
		return protocolObservationToken{}
	}
	var cursor [sha256.Size]byte
	var hasCursor, limited bool
	valid := true
	if req.Method == "tools/list" {
		cursor, hasCursor, valid, limited = observationCursor(req.Params)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation == "" || generation != o.details.ChildGeneration || o.details.ChildState != "running" || o.epochLimited {
		return protocolObservationToken{}
	}
	if req.Method == "initialize" {
		if o.details.InitializeEpoch == math.MaxUint64 {
			o.epochLimited = true
			o.details.Initialize = healthstate.MCPInitialize{Limited: true}
			o.resetCatalogLocked()
			o.state, o.reason = "not_observed", "initialize_epoch_limit"
			return protocolObservationToken{}
		}
		o.details.InitializeEpoch++
		o.details.Initialize = healthstate.MCPInitialize{}
		o.details.ServerDiscover = nil
		o.resetCatalogLocked()
		o.state, o.reason, o.observedAt = "not_observed", "", nil
		return protocolObservationToken{
			generation: generation, epoch: o.details.InitializeEpoch,
			serverDiscoverEpoch: o.details.ServerDiscoverEpoch, method: req.Method,
		}
	}
	if req.Method == "server/discover" {
		if protocolVersion == "" || o.serverDiscoverEpochLimited {
			return protocolObservationToken{}
		}
		if o.details.ServerDiscoverEpoch == math.MaxUint64 {
			o.serverDiscoverEpochLimited = true
			o.details.ServerDiscover = &healthstate.MCPServerDiscover{Limited: true}
			o.resetCatalogLocked()
			o.restoreProtocolStateLocked()
			o.reason = "server_discover_epoch_limit"
			o.observedAt = o.timestampLocked()
			return protocolObservationToken{}
		}
		o.details.ServerDiscoverEpoch++
		o.details.ServerDiscover = nil
		if !o.advanceCatalogLocked() {
			return protocolObservationToken{}
		}
		o.resetCatalogLocked()
		o.restoreProtocolStateLocked()
		if len(req.Params) > maxObservationParamsBytes {
			o.details.ServerDiscover = &healthstate.MCPServerDiscover{Limited: true}
			o.reason = "observation_params_limit"
			o.observedAt = o.timestampLocked()
			return protocolObservationToken{}
		}
		return protocolObservationToken{
			generation: generation, epoch: o.details.InitializeEpoch,
			serverDiscoverEpoch: o.details.ServerDiscoverEpoch, revision: o.catalogRevision,
			method: req.Method, protocolVersion: protocolVersion,
		}
	}
	if !o.details.Initialize.OK && protocolVersion == "" {
		return protocolObservationToken{}
	}
	if !o.advanceCatalogIfFirstLocked(hasCursor, valid, protocolVersion) {
		return protocolObservationToken{}
	}
	token := protocolObservationToken{
		generation: generation, epoch: o.details.InitializeEpoch,
		serverDiscoverEpoch: o.details.ServerDiscoverEpoch, revision: o.catalogRevision,
		method: req.Method, protocolVersion: protocolVersion,
	}
	if !valid {
		o.invalidateCatalogLocked("invalid_tools_cursor", limited)
		o.advanceCatalogLocked()
		return token
	}
	if !hasCursor {
		o.catalogActive = true
		o.catalogProtocolVersion = protocolVersion
	} else if protocolVersion != o.catalogProtocolVersion {
		o.invalidateCatalogLocked("tools_protocol_mismatch", false)
		o.advanceCatalogLocked()
		return token
	} else if !o.catalogActive || !o.hasNextCursor || cursor != o.nextCursor {
		o.invalidateCatalogLocked("tools_cursor_mismatch", false)
		o.advanceCatalogLocked()
		return token
	}
	if o.pages >= maxObservationPages {
		o.invalidateCatalogLocked("tools_page_limit", true)
		o.advanceCatalogLocked()
		return token
	}
	token.acceptPage = true
	return token
}

func (o *ProtocolObservation) advanceCatalogIfFirstLocked(hasCursor, valid bool, protocolVersion string) bool {
	if o.catalogLimited {
		return false
	}
	if !hasCursor && valid {
		if !o.advanceCatalogLocked() {
			return false
		}
		o.resetCatalogLocked()
		o.catalogProtocolVersion = protocolVersion
		o.restoreProtocolStateLocked()
	}
	return true
}

func (o *ProtocolObservation) restoreProtocolStateLocked() {
	switch {
	case o.details.ServerDiscover != nil && o.details.ServerDiscover.OK:
		o.state = "server_discovered"
		o.observedAt = copyObservationTime(o.details.ServerDiscover.ObservedAt)
	case o.details.Initialize.OK:
		o.state = "initialized"
		o.observedAt = copyObservationTime(o.details.Initialize.ObservedAt)
	default:
		o.state, o.observedAt = "not_observed", nil
	}
	o.reason = ""
}

func (o *ProtocolObservation) tokenMatchesLocked(token protocolObservationToken) bool {
	if token.generation == "" || token.generation != o.details.ChildGeneration || o.details.ChildState != "running" ||
		token.epoch != o.details.InitializeEpoch || o.epochLimited {
		return false
	}
	if token.method == "initialize" {
		return true
	}
	return token.serverDiscoverEpoch == o.details.ServerDiscoverEpoch && !o.serverDiscoverEpochLimited &&
		token.revision == o.catalogRevision && !o.catalogLimited
}

func (o *ProtocolObservation) response(token protocolObservationToken, response *jsonrpc.Response) {
	if o == nil || token.generation == "" || response == nil {
		return
	}
	if response.Error != nil {
		o.failed(token, "mcp_discovery_error", false)
		return
	}
	if len(response.Result) > maxObservationResultBytes {
		o.failed(token, "observation_result_limit", true)
		return
	}
	if token.method == "initialize" {
		result, valid := parseObservedInitialize(response.Result)
		if !valid {
			o.failed(token, "invalid_initialize_result", false)
			return
		}
		o.publishInitialize(token, result)
		return
	}
	if token.method == "server/discover" {
		result, valid := parseObservedServerDiscover(response.Result, token.protocolVersion)
		if !valid {
			o.failed(token, "invalid_server_discover_result", result.Limited)
			return
		}
		o.publishServerDiscover(token, result)
		return
	}
	if token.method == "tools/list" && token.acceptPage {
		page, valid := parseObservedTools(response.Result)
		if !valid {
			o.failed(token, "invalid_tools_result", page.limited)
			return
		}
		o.publishTools(token, page)
	}
}

func (o *ProtocolObservation) publishServerDiscover(token protocolObservationToken, result healthstate.MCPServerDiscover) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tokenMatchesLocked(token) {
		return
	}
	result.ObservedAt = o.timestampLocked()
	o.details.ServerDiscover = &result
	o.state, o.reason = "server_discovered", ""
	o.observedAt = copyObservationTime(result.ObservedAt)
}

func (o *ProtocolObservation) publishInitialize(token protocolObservationToken, result healthstate.MCPInitialize) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tokenMatchesLocked(token) {
		return
	}
	result.ObservedAt = o.timestampLocked()
	o.details.Initialize = result
	o.state, o.reason = "initialized", ""
	o.observedAt = copyObservationTime(result.ObservedAt)
}

func (o *ProtocolObservation) failed(token protocolObservationToken, reason string, limited bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tokenMatchesLocked(token) {
		return
	}
	switch token.method {
	case "tools/list":
		o.invalidateCatalogLocked(reason, limited)
		if !o.advanceCatalogLocked() {
			return
		}
	case "server/discover":
		o.details.ServerDiscover = &healthstate.MCPServerDiscover{Limited: limited}
		o.resetCatalogLocked()
		if !o.advanceCatalogLocked() {
			return
		}
	default:
		o.details.Initialize = healthstate.MCPInitialize{Limited: limited}
	}
	o.reason = reason
	o.observedAt = o.timestampLocked()
	if !limited {
		o.state = "failed"
	} else {
		o.restoreProtocolStateLocked()
		o.reason = reason
		o.observedAt = o.timestampLocked()
	}
}

func (o *ProtocolObservation) invalidateCatalogLocked(reason string, limited bool) {
	o.catalogActive = false
	o.hasNextCursor = false
	o.details.ToolsList.Complete = false
	o.details.ToolsList.Partial = true
	o.details.ToolsList.Limited = o.details.ToolsList.Limited || limited
	o.reason = reason
}

func (o *ProtocolObservation) listChanged(generation string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation != o.details.ChildGeneration || o.details.ChildState != "running" ||
		(!o.details.Initialize.OK && (o.details.ServerDiscover == nil || !o.details.ServerDiscover.OK) && !o.details.ToolsList.OK && !o.catalogActive) {
		return
	}
	if o.advanceCatalogLocked() {
		o.resetCatalogLocked()
		o.details.ToolsList.Partial = true
		o.restoreProtocolStateLocked()
		o.reason = "tools_list_changed"
		o.observedAt = o.timestampLocked()
	}
}

type observedToolsPage struct {
	names         []string
	nextCursor    [sha256.Size]byte
	hasNextCursor bool
	limited       bool
}

func (o *ProtocolObservation) publishTools(token protocolObservationToken, page observedToolsPage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tokenMatchesLocked(token) || !token.acceptPage || !o.catalogActive {
		return
	}
	names, limited := boundedObservationNames(append(slices.Clone(o.details.ToolsList.ToolNames), page.names...))
	tools := &o.details.ToolsList
	tools.OK = true
	tools.ObservedAt = o.timestampLocked()
	tools.ToolNames, tools.RetainedCount = names, len(names)
	tools.Limited = tools.Limited || page.limited || limited
	o.pages++
	o.hasNextCursor, o.nextCursor = page.hasNextCursor, page.nextCursor
	o.state, o.reason, o.observedAt = "discovered", "", copyObservationTime(tools.ObservedAt)
	if page.hasNextCursor {
		if _, seen := o.seenCursors[page.nextCursor]; seen {
			o.invalidateCatalogLocked("tools_cursor_cycle", false)
			return
		}
		if o.seenCursors == nil {
			o.seenCursors = make(map[[sha256.Size]byte]struct{})
		}
		o.seenCursors[page.nextCursor] = struct{}{}
	}
	tools.Complete = !page.hasNextCursor && !tools.Limited
	tools.Partial = !tools.Complete
	if !page.hasNextCursor {
		o.catalogActive = false
	}
}

func observationCursor(params json.RawMessage) ([sha256.Size]byte, bool, bool, bool) {
	if len(params) > maxObservationParamsBytes {
		return [sha256.Size]byte{}, false, false, true
	}
	if len(params) == 0 || string(params) == "null" {
		return [sha256.Size]byte{}, false, true, false
	}
	var request struct {
		Cursor json.RawMessage `json:"cursor"`
	}
	if json.Unmarshal(params, &request) != nil {
		return [sha256.Size]byte{}, false, false, false
	}
	return decodeObservationCursor(request.Cursor)
}

func decodeObservationCursor(raw json.RawMessage) ([sha256.Size]byte, bool, bool, bool) {
	if len(raw) == 0 {
		return [sha256.Size]byte{}, false, true, false
	}
	var cursor string
	if string(raw) == "null" || !validObservationUnicode(raw) || json.Unmarshal(raw, &cursor) != nil {
		return [sha256.Size]byte{}, false, false, false
	}
	if len(cursor) > maxObservationCursorBytes {
		return [sha256.Size]byte{}, true, false, true
	}
	return sha256.Sum256([]byte(cursor)), true, true, false
}

func parseObservedServerDiscover(raw json.RawMessage, requestedVersion string) (healthstate.MCPServerDiscover, bool) {
	var result struct {
		ResultType        json.RawMessage            `json:"resultType"`
		SupportedVersions *[]string                  `json:"supportedVersions"`
		Capabilities      map[string]json.RawMessage `json:"capabilities"`
	}
	if !validObservationUnicode(raw) || json.Unmarshal(raw, &result) != nil ||
		result.SupportedVersions == nil || len(*result.SupportedVersions) == 0 || result.Capabilities == nil {
		return healthstate.MCPServerDiscover{}, false
	}
	// Older discovery implementations omitted the discriminator. If supplied,
	// it must be the only currently complete response shape.
	if len(result.ResultType) > 0 {
		var resultType *string
		if json.Unmarshal(result.ResultType, &resultType) != nil || resultType == nil || *resultType != "complete" {
			return healthstate.MCPServerDiscover{}, false
		}
	}
	capabilityNames, valid := observedCapabilityNames(result.Capabilities)
	if !valid {
		return healthstate.MCPServerDiscover{}, false
	}
	observed := healthstate.MCPServerDiscover{
		OK: true, SupportedVersions: make([]string, 0, min(len(*result.SupportedVersions), maxObservationVersions)),
		CapabilityNames: capabilityNames,
	}
	containsRequested := false
	for _, version := range *result.SupportedVersions {
		if version == "" {
			return healthstate.MCPServerDiscover{}, false
		}
		if _, err := time.Parse(time.DateOnly, version); err != nil {
			return healthstate.MCPServerDiscover{}, false
		}
		containsRequested = containsRequested || version == requestedVersion
		if len(version) > maxObservationIdentity {
			observed.Limited = true
			continue
		}
		observed.SupportedVersions = append(observed.SupportedVersions, strings.Clone(version))
	}
	slices.Sort(observed.SupportedVersions)
	observed.SupportedVersions = slices.Compact(observed.SupportedVersions)
	if len(observed.SupportedVersions) > maxObservationVersions {
		observed.SupportedVersions = observed.SupportedVersions[:maxObservationVersions]
		observed.Limited = true
	}
	if containsRequested && !slices.Contains(observed.SupportedVersions, requestedVersion) {
		observed.SupportedVersions[len(observed.SupportedVersions)-1] = strings.Clone(requestedVersion)
		slices.Sort(observed.SupportedVersions)
	}
	// Do not retain the backing array for discarded or duplicate versions.
	observed.SupportedVersions = slices.Clone(observed.SupportedVersions)
	return observed, true
}

func parseObservedInitialize(raw json.RawMessage) (healthstate.MCPInitialize, bool) {
	var result struct {
		ProtocolVersion *string `json:"protocolVersion"`
		ServerInfo      *struct {
			Name    *string `json:"name"`
			Version *string `json:"version"`
		} `json:"serverInfo"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if !validObservationUnicode(raw) || json.Unmarshal(raw, &result) != nil || result.ProtocolVersion == nil || *result.ProtocolVersion == "" ||
		result.ServerInfo == nil || result.ServerInfo.Name == nil || *result.ServerInfo.Name == "" || result.ServerInfo.Version == nil || result.Capabilities == nil {
		return healthstate.MCPInitialize{}, false
	}
	observed := healthstate.MCPInitialize{OK: true, IdentityComplete: true, CapabilityNames: []string{}}
	for _, field := range []struct {
		source string
		target *string
	}{
		{*result.ProtocolVersion, &observed.ProtocolVersion}, {*result.ServerInfo.Name, &observed.ServerName}, {*result.ServerInfo.Version, &observed.ServerVersion},
	} {
		if len(field.source) > maxObservationIdentity {
			observed.Limited, observed.IdentityComplete = true, false
		} else {
			*field.target = field.source
		}
	}
	capabilityNames, valid := observedCapabilityNames(result.Capabilities)
	if !valid {
		return healthstate.MCPInitialize{}, false
	}
	observed.CapabilityNames = capabilityNames
	return observed, true
}

func observedCapabilityNames(capabilities map[string]json.RawMessage) ([]string, bool) {
	names := []string{}
	for _, capability := range []string{"completions", "logging", "prompts", "resources", "tasks", "tools"} {
		value, found := capabilities[capability]
		if !found {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil || object == nil {
			return nil, false
		}
		names = append(names, capability)
	}
	return names, true
}

func parseObservedTools(raw json.RawMessage) (observedToolsPage, bool) {
	var result struct {
		Tools *[]struct {
			Name *string `json:"name"`
		} `json:"tools"`
		NextCursor json.RawMessage `json:"nextCursor"`
	}
	if !validObservationUnicode(raw) || json.Unmarshal(raw, &result) != nil || result.Tools == nil {
		return observedToolsPage{}, false
	}
	page := observedToolsPage{}
	var valid bool
	page.nextCursor, page.hasNextCursor, valid, page.limited = decodeObservationCursor(result.NextCursor)
	if !valid {
		return page, false
	}
	for _, tool := range *result.Tools {
		if tool.Name == nil || *tool.Name == "" {
			return observedToolsPage{}, false
		}
		if len(*tool.Name) > maxObservationToolBytes {
			page.limited = true
			continue
		}
		page.names = append(page.names, *tool.Name)
	}
	page.names, valid = boundedObservationNames(page.names)
	page.limited = page.limited || valid
	return page, true
}

// encoding/json replaces malformed UTF-8 and unpaired UTF-16 escape sequences
// with U+FFFD. Reject them in diagnostic projections rather than reporting an
// invented exact server identity, tool name, or pagination cursor.
func validObservationUnicode(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func boundedObservationNames(names []string) ([]string, bool) {
	slices.Sort(names)
	names = slices.Compact(names)
	limited := len(names) > maxObservationToolNames
	if limited {
		names = names[:maxObservationToolNames]
	}
	bytes := 2 // JSON array delimiters.
	for i, name := range names {
		encoded, _ := json.Marshal(name)
		size := len(encoded)
		if i > 0 {
			size++
		}
		if bytes+size > maxObservationNamesJSON {
			names, limited = names[:i], true
			break
		}
		bytes += size
	}
	retained := make([]string, len(names))
	for i, name := range names {
		retained[i] = strings.Clone(name)
	}
	return retained, limited
}

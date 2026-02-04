package internal

import (
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	"go.openai.org/api/tunnel-client/pkg/types"
)

var (
	_ controlplane.PolledCommand         = (*oauthDiscoveryCommand)(nil)
	_ controlplane.OauthDiscoveryCommand = (*oauthDiscoveryCommand)(nil)
	_ controlplane.PolledCommand         = (*jsonRpcCommand)(nil)
	_ controlplane.JsonRpcCommand        = (*jsonRpcCommand)(nil)
	_ typedCommand                       = (*oauthDiscoveryCommand)(nil)
	_ typedCommand                       = (*jsonRpcCommand)(nil)
)

// typedCommand narrows commands to those with a known discriminator.
type typedCommand interface {
	controlplane.PolledCommand
	commandType() wiretypes.CommandType
}

// basePolledCommand contains fields common to all polled command implementations
// and implements the internal.PolledCommand interface.
type basePolledCommand struct {
	requestID  types.RequestID
	enqueued   time.Time
	polledAt   time.Time
	headers    http.Header
	sessionID  *string
	shardToken string
	channel    types.Channel
}

func (c *basePolledCommand) RequestID() types.RequestID { return c.requestID }
func (c *basePolledCommand) EnqueuedAt() time.Time      { return c.enqueued }
func (c *basePolledCommand) PolledAt() time.Time        { return c.polledAt }
func (c *basePolledCommand) Headers() http.Header {
	if c.headers == nil {
		return nil
	}
	return c.headers
}
func (c *basePolledCommand) ShardToken() string     { return c.shardToken }
func (c *basePolledCommand) Channel() types.Channel { return c.channel }
func (c *basePolledCommand) SessionID() (string, bool) {
	if c.sessionID == nil {
		return "", false
	}
	return *c.sessionID, true
}

// commandType returns an empty discriminator for the base type; concrete
// commands must override this.
func (c *basePolledCommand) commandType() wiretypes.CommandType { return "" }

// jsonRpcCommand represents a JSON-RPC command; it implements JsonRpcCommand via Message().
type jsonRpcCommand struct {
	basePolledCommand
	message jsonrpc.Message
}

// Message is only implemented by jsonRpcCommand
func (c *jsonRpcCommand) Message() jsonrpc.Message { return c.message }
func (c *jsonRpcCommand) commandType() wiretypes.CommandType {
	return wiretypes.CommandTypeJSONRPC
}

// oauthDiscoveryCommand represents a non-JSON-RPC command (OAuth discovery).
// It intentionally does NOT include a Message() method so it will not satisfy
// JsonRpcCommand, allowing the dispatcher to distinguish the type.
type oauthDiscoveryCommand struct {
	basePolledCommand
}

func (c *oauthDiscoveryCommand) commandType() wiretypes.CommandType {
	return wiretypes.CommandTypeOAuthDiscovery
}

func (c *oauthDiscoveryCommand) IsOAuthDiscovery() bool { return true }

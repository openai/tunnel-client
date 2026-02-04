package dispatcher

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/types"
)

type noopTransport struct{}

func (noopTransport) Connect(context.Context) (mcp.Connection, error) { return nil, nil }

func TestNewProcessorChannelBindingsSuccess(t *testing.T) {
	t.Parallel()

	bindings, err := newProcessorChannelBindings(processorChannelBindingsParams{
		Bindings: []dispatcherChannelBinding{
			{
				Channel:       types.Channel(" MAIN "),
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
			{
				Channel:       types.Channel("harpoon"),
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: false,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, bindings, 2)
	mainBinding, ok := bindings[types.DefaultChannel]
	require.True(t, ok)
	require.NotNil(t, mainBinding.Transport)
	require.True(t, mainBinding.SupportsMCP)
	require.True(t, mainBinding.SupportsOAuth)

	harpoonBinding, ok := bindings[types.ChannelHarpoon]
	require.True(t, ok)
	require.NotNil(t, harpoonBinding.Transport)
	require.True(t, harpoonBinding.SupportsMCP)
	require.False(t, harpoonBinding.SupportsOAuth)
}

func TestNewProcessorChannelBindingsMissingRequired(t *testing.T) {
	t.Parallel()

	_, err := newProcessorChannelBindings(processorChannelBindingsParams{
		Bindings: []dispatcherChannelBinding{
			{
				Channel:       types.DefaultChannel,
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "missing required channels")
	require.ErrorContains(t, err, "required channels")
}

func TestNewProcessorChannelBindingsRejectsDuplicateNormalizedChannel(t *testing.T) {
	t.Parallel()

	_, err := newProcessorChannelBindings(processorChannelBindingsParams{
		Bindings: []dispatcherChannelBinding{
			{
				Channel:       types.DefaultChannel,
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
			{
				Channel:       types.Channel("harpoon"),
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: false,
			},
			{
				Channel:       types.Channel(" HARPOON "),
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: false,
			},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "duplicate channel")
}

func TestNewProcessorChannelBindingsRejectsNonMainOAuth(t *testing.T) {
	t.Parallel()

	_, err := newProcessorChannelBindings(processorChannelBindingsParams{
		Bindings: []dispatcherChannelBinding{
			{
				Channel:       types.DefaultChannel,
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
			{
				Channel:       types.ChannelHarpoon,
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "non-main channel")
}

func TestNewProcessorChannelBindingsRejectsEmptyNormalizedChannel(t *testing.T) {
	t.Parallel()

	_, err := newProcessorChannelBindings(processorChannelBindingsParams{
		Bindings: []dispatcherChannelBinding{
			{
				Channel:       types.DefaultChannel,
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
			{
				Channel:       types.ChannelHarpoon,
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: false,
			},
			{
				Channel:       types.Channel("   "),
				Priority:      0,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: false,
			},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid after normalization")
}

func TestNewProcessorChannelBindingsProducesInternalBindingType(t *testing.T) {
	t.Parallel()

	bindings, err := newProcessorChannelBindings(processorChannelBindingsParams{
		Bindings: []dispatcherChannelBinding{
			{
				Channel:       types.DefaultChannel,
				Priority:      3,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: true,
			},
			{
				Channel:       types.ChannelHarpoon,
				Priority:      7,
				Transport:     noopTransport{},
				SupportsMCP:   true,
				SupportsOAuth: false,
			},
		},
	})
	require.NoError(t, err)

	require.Equal(t, 3, bindings[types.DefaultChannel].Priority)
	require.Equal(t, 7, bindings[types.ChannelHarpoon].Priority)
}

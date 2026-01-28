package harpoon

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryRejectsInvalidLabel(t *testing.T) {
	registry, err := NewRegistry(true, nil)
	require.NoError(t, err)

	parsed, err := url.Parse("https://example.com")
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{Label: "bad label", BaseURL: parsed})
	require.Error(t, err)
}

func TestRegistryRejectsDuplicateLabel(t *testing.T) {
	parsed, err := url.Parse("https://example.com")
	require.NoError(t, err)

	registry, err := NewRegistry(true, []Target{{Label: "auth", BaseURL: parsed}})
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{Label: "auth", BaseURL: parsed})
	require.Error(t, err)
}

func TestRegistryRejectsPlaintextWhenDisallowed(t *testing.T) {
	parsed, err := url.Parse("http://example.com")
	require.NoError(t, err)

	_, err = NewRegistry(false, []Target{{Label: "auth", BaseURL: parsed}})
	require.Error(t, err)
}

func TestSummarizeTargets(t *testing.T) {
	urlA, err := url.Parse("https://example.com/base")
	require.NoError(t, err)
	urlB, err := url.Parse("https://example.org")
	require.NoError(t, err)

	registry, err := NewRegistry(true, []Target{
		{Label: "auth", Description: "Auth server", BaseURL: urlA},
		{Label: "idp", Description: "Identity", BaseURL: urlB},
	})
	require.NoError(t, err)

	summary := registry.SummarizeTargets()

	require.Equal(t, []map[string]string{
		{"label": "auth", "url": "https://example.com/base", "desc": "Auth server"},
		{"label": "idp", "url": "https://example.org/", "desc": "Identity"},
	}, summary)
}

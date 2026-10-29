package runtimeconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type exchangeTransport func(*http.Request) (*http.Response, error)

func (f exchangeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAWSWorkloadIdentityExchangesAndCachesTokens(t *testing.T) {
	// Use only fixture credentials. Exercise both real SDKs against protocol fixtures.
	configDir := t.TempDir()
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(configDir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(configDir, "credentials"))
	t.Setenv("AWS_REGION", "us-east-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-secret")
	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "invalid form", 400)
			return
		}
		if r.Form.Get("Action") != "GetWebIdentityToken" || r.Form.Get("Audience.member.1") != "https://api.openai.com/v1" || r.Form.Get("DurationSeconds") != "300" || r.Form.Get("SigningAlgorithm") != "RS256" || !strings.Contains(r.Header.Get("Authorization"), "Credential=fixture-id/") {
			t.Error("invalid signed STS request")
		}
		if _, err := fmt.Fprint(w, `<GetWebIdentityTokenResponse><GetWebIdentityTokenResult><WebIdentityToken>fixture.jwt.signature</WebIdentityToken></GetWebIdentityTokenResult></GetWebIdentityTokenResponse>`); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(stsServer.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", stsServer.URL)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	exchanges := 0
	http.DefaultTransport = exchangeTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https://auth.openai.com/oauth/token", r.URL.String())
		require.Equal(t, http.MethodPost, r.Method)
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "fixture.jwt.signature", payload["subject_token"])
		require.Equal(t, "provider", payload["identity_provider_id"])
		require.Equal(t, "sa", payload["service_account_id"])
		exchanges++
		ttl := 3600
		if exchanges == 1 {
			ttl = 0
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":"token-%d","expires_in":%d}`, exchanges, ttl))), Header: make(http.Header)}, nil
	})
	endpoint, err := url.Parse(DefaultControlPlaneBaseURL)
	require.NoError(t, err)
	provider, err := controlPlaneTokenProvider("", "provider", "sa", endpoint)
	require.NoError(t, err)
	for _, want := range []string{"token-1", "token-2", "token-2"} {
		token, err := provider(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, token)
	}
	require.Equal(t, 2, exchanges)
}

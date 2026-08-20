package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func selectedAuthServerMetadataAttempt(t *testing.T, attempts []AuthServerMetadataAttempt) AuthServerMetadataAttempt {
	t.Helper()

	selectedIndex := -1
	for i, attempt := range attempts {
		if !attempt.Selected {
			continue
		}
		if selectedIndex >= 0 {
			t.Fatalf("expected a single selected attempt, found multiple (indexes %d and %d)", selectedIndex, i)
		}
		selectedIndex = i
	}
	if selectedIndex < 0 {
		t.Fatalf("expected a selected attempt")
	}
	return attempts[selectedIndex]
}

func authServerMetadataAttemptByURL(
	t *testing.T,
	attempts []AuthServerMetadataAttempt,
	attemptURL string,
) AuthServerMetadataAttempt {
	t.Helper()

	for _, attempt := range attempts {
		if attempt.URL == attemptURL {
			return attempt
		}
	}
	t.Fatalf("attempt for URL %q not found", attemptURL)
	return AuthServerMetadataAttempt{}
}

func TestFetchAuthServerMetadata(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	payload := map[string]any{
		"issuer":                 server.URL,
		"authorization_endpoint": server.URL + "/authorize",
		"token_endpoint":         server.URL + "/token",
		"jwks_uri":               server.URL + "/jwks",
		"registration_endpoint":  server.URL + "/register",
		"revocation_endpoint":    server.URL + "/revoke",
		"introspection_endpoint": server.URL + "/introspect",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	meta, err := FetchAuthServerMetadata(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("FetchAuthServerMetadata returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected metadata")
		return
	}
	if meta.Issuer != server.URL {
		t.Fatalf("issuer mismatch: got %q want %q", meta.Issuer, server.URL)
	}
	if meta.AuthorizationEndpoint != server.URL+"/authorize" {
		t.Fatalf("authorization_endpoint mismatch: got %q", meta.AuthorizationEndpoint)
	}
	if meta.TokenEndpoint != server.URL+"/token" {
		t.Fatalf("token_endpoint mismatch: got %q", meta.TokenEndpoint)
	}
	if meta.JWKSURI != server.URL+"/jwks" {
		t.Fatalf("jwks_uri mismatch: got %q", meta.JWKSURI)
	}
	if meta.IntrospectionEndpoint != server.URL+"/introspect" {
		t.Fatalf("introspection_endpoint mismatch: got %q", meta.IntrospectionEndpoint)
	}
	if meta.RegistrationEndpoint != server.URL+"/register" {
		t.Fatalf("registration_endpoint mismatch: got %q", meta.RegistrationEndpoint)
	}
	if meta.RevocationEndpoint != server.URL+"/revoke" {
		t.Fatalf("revocation_endpoint mismatch: got %q", meta.RevocationEndpoint)
	}
}

func TestFetchAuthServerMetadataFallsBackToOIDCWellKnown(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	payload := map[string]any{
		"issuer":                 server.URL,
		"authorization_endpoint": server.URL + "/authorize",
		"token_endpoint":         server.URL + "/token",
		"jwks_uri":               server.URL + "/jwks",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	meta, err := FetchAuthServerMetadata(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("FetchAuthServerMetadata returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected metadata")
		return
	}
	if meta.Issuer != server.URL {
		t.Fatalf("issuer mismatch: got %q want %q", meta.Issuer, server.URL)
	}
}

func TestFetchAuthServerMetadataRejectsCrossOriginRedirectBeforeDial(t *testing.T) {
	t.Parallel()

	attackerHits := 0
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attackerHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"https://attacker.invalid"}`))
	}))
	t.Cleanup(attacker.Close)

	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/metadata", http.StatusFound)
	}))
	t.Cleanup(issuer.Close)

	_, _, err := FetchAuthServerMetadataWithResult(context.Background(), issuer.Client(), issuer.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirect blocked")
	require.Zero(t, attackerHits)
}

func TestFetchAuthServerMetadataSupportsAppendStyleOIDCPath(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuerURL := server.URL + "/tenant/v2.0"
	payload := map[string]any{
		"issuer":                 issuerURL,
		"authorization_endpoint": issuerURL + "/authorize",
		"token_endpoint":         issuerURL + "/token",
		"jwks_uri":               issuerURL + "/jwks",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	// Simulates providers like Azure AD that serve:
	//   /{tenant}/v2.0/.well-known/openid-configuration
	mux.HandleFunc("/tenant/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
	})

	meta, err := FetchAuthServerMetadata(context.Background(), server.Client(), issuerURL)
	if err != nil {
		t.Fatalf("FetchAuthServerMetadata returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected metadata")
		return
	}
	if meta.Issuer != issuerURL {
		t.Fatalf("issuer mismatch: got %q want %q", meta.Issuer, issuerURL)
	}
	if meta.TokenEndpoint != issuerURL+"/token" {
		t.Fatalf("token_endpoint mismatch: got %q", meta.TokenEndpoint)
	}
}

func TestFetchAuthServerMetadataWithResultIncludesAttempts(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuerURL := server.URL + "/tenant/v2.0"
	successPath := "/tenant/v2.0/.well-known/openid-configuration"

	mux.HandleFunc("/.well-known/oauth-authorization-server/tenant/v2.0", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc("/tenant/v2.0/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc("/.well-known/openid-configuration/tenant/v2.0", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc(successPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer":"` + issuerURL + `",
			"token_endpoint":"` + issuerURL + `/token"
		}`))
	})

	meta, result, err := FetchAuthServerMetadataWithResult(context.Background(), server.Client(), issuerURL)
	if err != nil {
		t.Fatalf("FetchAuthServerMetadataWithResult returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected metadata")
		return
	}
	if result == nil {
		t.Fatalf("expected fetch result")
		return
	}
	if result.SelectedURL != server.URL+successPath {
		t.Fatalf("unexpected selected URL: got %q want %q", result.SelectedURL, server.URL+successPath)
	}
	if len(result.Attempts) == 0 {
		t.Fatalf("expected attempts to be populated")
	}
	last := result.Attempts[len(result.Attempts)-1]
	if !last.Selected {
		t.Fatalf("expected final attempt to be selected")
	}
	if last.PathStyle != "append" {
		t.Fatalf("unexpected path style: got %q want %q", last.PathStyle, "append")
	}
	if last.Document != "openid-configuration" {
		t.Fatalf("unexpected document: got %q want %q", last.Document, "openid-configuration")
	}
	if last.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d", last.StatusCode, http.StatusOK)
	}
	if len(last.Body) == 0 {
		t.Fatalf("expected selected attempt body")
	}
}

func TestFetchAuthServerMetadataWithResultAcceptsIssuerMismatch(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuerURL := server.URL
	metadataIssuer := "https://idp.bigco-example.com/oauth2/aus2jrb9zi4O8hseE0h8"
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer":"` + metadataIssuer + `",
			"token_endpoint":"` + metadataIssuer + `/token",
			"registration_endpoint":"` + metadataIssuer + `/register",
			"jwks_uri":"` + metadataIssuer + `/jwks"
		}`))
	})

	meta, result, err := FetchAuthServerMetadataWithResult(context.Background(), server.Client(), issuerURL)
	if err != nil {
		t.Fatalf("FetchAuthServerMetadataWithResult returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected metadata")
		return
	}
	if result == nil {
		t.Fatalf("expected fetch result")
		return
	}
	if result.SelectedURL != server.URL+"/.well-known/oauth-authorization-server" {
		t.Fatalf(
			"unexpected selected URL: got %q want %q",
			result.SelectedURL,
			server.URL+"/.well-known/oauth-authorization-server",
		)
	}
	if meta.Issuer != metadataIssuer {
		t.Fatalf("unexpected metadata issuer: got %q want %q", meta.Issuer, metadataIssuer)
	}
	if len(result.Attempts) == 0 {
		t.Fatalf("expected attempts to be populated")
	}
	selected := selectedAuthServerMetadataAttempt(t, result.Attempts)
	if !selected.IssuerMismatch {
		t.Fatalf("expected issuer mismatch diagnostics on selected attempt")
	}
	if selected.ExpectedIssuerURL != issuerURL {
		t.Fatalf("unexpected expected issuer URL: got %q want %q", selected.ExpectedIssuerURL, issuerURL)
	}
	if selected.MetadataIssuer != metadataIssuer {
		t.Fatalf("unexpected metadata issuer diagnostic: got %q want %q", selected.MetadataIssuer, metadataIssuer)
	}
	if selected.Warning == "" {
		t.Fatalf("expected warning message for issuer mismatch")
	}
	if selected.Error != "" {
		t.Fatalf("did not expect hard error for issuer mismatch, got %q", selected.Error)
	}
}

func TestFetchAuthServerMetadataWithResultPrefersExactIssuerMatchOverMismatch(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuerURL := server.URL + "/tenant/v2.0"
	mismatchIssuer := "https://idp.bigco-example.com/oauth2/aus2jrb9zi4O8hseE0h8"

	appendOAuthPath := "/tenant/v2.0/.well-known/oauth-authorization-server"
	prependOAuthPath := "/.well-known/oauth-authorization-server/tenant/v2.0"
	appendOIDCPath := "/tenant/v2.0/.well-known/openid-configuration"
	prependOIDCPath := "/.well-known/openid-configuration/tenant/v2.0"

	var appendOAuthRequests int
	var prependOAuthRequests int
	var openIDRequests int

	mux.HandleFunc(appendOAuthPath, func(w http.ResponseWriter, _ *http.Request) {
		appendOAuthRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer":"` + mismatchIssuer + `",
			"token_endpoint":"` + mismatchIssuer + `/token"
		}`))
	})
	mux.HandleFunc(prependOAuthPath, func(w http.ResponseWriter, _ *http.Request) {
		prependOAuthRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer":"` + issuerURL + `",
			"token_endpoint":"` + issuerURL + `/token"
		}`))
	})
	mux.HandleFunc(appendOIDCPath, func(w http.ResponseWriter, _ *http.Request) {
		openIDRequests++
		http.NotFound(w, nil)
	})
	mux.HandleFunc(prependOIDCPath, func(w http.ResponseWriter, _ *http.Request) {
		openIDRequests++
		http.NotFound(w, nil)
	})

	meta, result, err := FetchAuthServerMetadataWithResult(context.Background(), server.Client(), issuerURL)
	if err != nil {
		t.Fatalf("FetchAuthServerMetadataWithResult returned error: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected metadata")
		return
	}
	if result == nil {
		t.Fatalf("expected fetch result")
		return
	}
	if result.SelectedURL != server.URL+prependOAuthPath {
		t.Fatalf("unexpected selected URL: got %q want %q", result.SelectedURL, server.URL+prependOAuthPath)
	}
	if meta.Issuer != issuerURL {
		t.Fatalf("unexpected metadata issuer: got %q want %q", meta.Issuer, issuerURL)
	}
	if meta.TokenEndpoint != issuerURL+"/token" {
		t.Fatalf("unexpected token endpoint: got %q want %q", meta.TokenEndpoint, issuerURL+"/token")
	}
	if appendOAuthRequests != 1 {
		t.Fatalf("expected mismatch candidate to be requested once, got %d", appendOAuthRequests)
	}
	if prependOAuthRequests != 1 {
		t.Fatalf("expected exact candidate to be requested once, got %d", prependOAuthRequests)
	}
	if openIDRequests != 0 {
		t.Fatalf("expected no openid fallback requests after exact issuer match, got %d", openIDRequests)
	}

	mismatchAttempt := authServerMetadataAttemptByURL(t, result.Attempts, server.URL+appendOAuthPath)
	if !mismatchAttempt.IssuerMismatch {
		t.Fatalf("expected issuer mismatch diagnostics on first successful candidate")
	}
	if mismatchAttempt.Selected {
		t.Fatalf("expected mismatch candidate to remain unselected when exact match exists")
	}

	selectedAttempt := selectedAuthServerMetadataAttempt(t, result.Attempts)
	if selectedAttempt.URL != server.URL+prependOAuthPath {
		t.Fatalf("unexpected selected attempt URL: got %q want %q", selectedAttempt.URL, server.URL+prependOAuthPath)
	}
	if selectedAttempt.IssuerMismatch {
		t.Fatalf("did not expect issuer mismatch diagnostics on exact-match attempt")
	}
}

func TestFetchAuthServerMetadataRetriesOnlyAfterAllTimeouts(t *testing.T) {
	issuerURL := "https://issuer-user-secret:issuer-password-secret@issuer.example.com/issuer-path-secret?issuer-query-key=issuer-query-secret#issuer-fragment-secret"
	parsedIssuer, err := url.Parse(issuerURL)
	if err != nil {
		t.Fatalf("parse issuer URL: %v", err)
	}

	candidates, err := buildAuthServerMetadataCandidateURLs(issuerURL)
	if err != nil {
		t.Fatalf("build candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatalf("expected candidates")
	}

	var calls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Host != parsedIssuer.Host {
				t.Fatalf("unexpected host: got %q want %q", req.URL.Host, parsedIssuer.Host)
			}
			return nil, context.DeadlineExceeded
		}),
	}

	meta, result, fetchErr := FetchAuthServerMetadataWithResult(context.Background(), client, issuerURL)
	if fetchErr == nil {
		t.Fatal("expected fetch error")
	}
	if meta != nil {
		t.Fatal("expected nil metadata on timeout failures")
	}
	if result == nil {
		t.Fatal("expected result")
		return
	}
	if len(result.Attempts) != len(candidates) {
		t.Fatalf("expected attempts from retry pass only: got %d want %d", len(result.Attempts), len(candidates))
	}

	expectedCalls := len(candidates) + len(candidates)*oauthMetadataRequestRetryCount
	if calls != expectedCalls {
		t.Fatalf("unexpected call count: got %d want %d", calls, expectedCalls)
	}
	if !errors.Is(fetchErr, context.DeadlineExceeded) {
		t.Fatalf("expected joined timeout error, got %v", fetchErr)
	}
	for _, sensitive := range []string{
		"issuer-user-secret",
		"issuer-password-secret",
		"issuer-path-secret",
		"issuer-query-key",
		"issuer-query-secret",
		"issuer-fragment-secret",
	} {
		if strings.Contains(fetchErr.Error(), sensitive) {
			t.Fatalf("fetch error contains sensitive issuer value %q: %v", sensitive, fetchErr)
		}
	}
	for _, attempt := range result.Attempts {
		if attempt.Error == "" {
			t.Fatalf("expected timeout error in attempt %+v", attempt)
		}
		for _, sensitive := range []string{
			"issuer-user-secret",
			"issuer-password-secret",
			"issuer-path-secret",
			"issuer-query-key",
			"issuer-query-secret",
			"issuer-fragment-secret",
		} {
			if strings.Contains(attempt.Error, sensitive) {
				t.Fatalf("attempt error contains sensitive issuer value %q: %q", sensitive, attempt.Error)
			}
		}
	}
}

func TestFetchAuthServerMetadataInvalidIssuerPreservesURLError(t *testing.T) {
	const issuerURL = "https://invalid-user-secret:invalid-password-secret@issuer.example.com/invalid-path-secret%zz?invalid-query-secret=value#invalid-fragment-secret"

	_, _, fetchErr := FetchAuthServerMetadataWithResult(context.Background(), http.DefaultClient, issuerURL)
	if fetchErr == nil {
		t.Fatal("expected invalid issuer error")
	}
	var urlErr *url.Error
	if !errors.As(fetchErr, &urlErr) {
		t.Fatalf("expected wrapped *url.Error, got %T: %v", fetchErr, fetchErr)
	}
	for _, sensitive := range []string{
		"invalid-user-secret",
		"invalid-password-secret",
		"invalid-path-secret",
		"invalid-query-secret",
		"invalid-fragment-secret",
	} {
		if strings.Contains(fetchErr.Error(), sensitive) {
			t.Fatalf("invalid issuer error contains sensitive value %q: %v", sensitive, fetchErr)
		}
	}
}

func TestBuildAuthServerMetadataCandidateURLsPathfulIssuerPrefersAppendStyle(t *testing.T) {
	issuerURL := "https://example.com/tenant/v2.0"
	candidates, err := buildAuthServerMetadataCandidateURLs(issuerURL)
	if err != nil {
		t.Fatalf("build candidates: %v", err)
	}
	if len(candidates) < 4 {
		t.Fatalf("expected at least 4 candidates, got %d", len(candidates))
	}

	firstByDoc := map[AuthServerMetadataDocument]AuthServerMetadataPathStyle{}
	for _, candidate := range candidates {
		if _, seen := firstByDoc[candidate.Document]; seen {
			continue
		}
		firstByDoc[candidate.Document] = candidate.PathStyle
	}
	for _, doc := range []AuthServerMetadataDocument{
		AuthServerMetadataDocumentOAuthAuthorizationServer,
		AuthServerMetadataDocumentOpenIDConfiguration,
	} {
		style, ok := firstByDoc[doc]
		if !ok {
			t.Fatalf("missing candidate for document %q", doc)
		}
		if style != AuthServerMetadataPathStyleAppend {
			t.Fatalf("first path style for %q = %q, want %q", doc, style, AuthServerMetadataPathStyleAppend)
		}
	}
}

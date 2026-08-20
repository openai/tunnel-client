package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/openai/tunnel-client/pkg/harpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/transport"
)

func TestBuildURLBundleFromPRMD(t *testing.T) {
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource: "https://resource.internal/",
		AuthorizationServers: []string{
			"https://auth1.internal/",
			"https://auth2.internal/",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		nil,
		payload,
		time.Unix(42, 0).UTC(),
		mustParseURL(t, "https://prmd.internal/.well-known/oauth-protected-resource"),
		URLBundleOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if len(bundle.URLs) != 3 {
		t.Fatalf("expected 3 urls, got %d", len(bundle.URLs))
	}

	if got := bundle.URLs[0].URL.String(); got != "https://resource.internal/" {
		t.Fatalf("unexpected resource url: %q", got)
	}
	if got := bundle.URLs[1].URL.String(); got != "https://auth1.internal/" {
		t.Fatalf("unexpected auth1 url: %q", got)
	}
	if got := bundle.URLs[2].URL.String(); got != "https://prmd.internal/.well-known/oauth-protected-resource" {
		t.Fatalf("unexpected source url: %q", got)
	}

	if len(bundle.URLs[0].Tags) != 3 {
		t.Fatalf("expected tags for resource")
	}
}

func TestBuildURLBundleFromPRMDEmpty(t *testing.T) {
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(context.Background(), nil, payload, time.Now(), nil, URLBundleOptions{}, nil); err == nil {
		t.Fatalf("expected error for empty metadata")
	}
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadata(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuer := server.URL + "/issuer-a"
	resource := server.URL + "/resource"
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource:             resource,
		AuthorizationServers: []string{issuer},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	metaBody, err := json.Marshal(map[string]any{
		"issuer":                 issuer,
		"authorization_endpoint": issuer + "/authorize",
		"token_endpoint":         issuer + "/token",
		"jwks_uri":               issuer + "/jwks",
		"introspection_endpoint": issuer + "/introspect",
		"registration_endpoint":  issuer + "/register",
		"revocation_endpoint":    issuer + "/revoke",
	})
	if err != nil {
		t.Fatalf("marshal metadata body: %v", err)
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer-a", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(metaBody)
	})

	sourceURL := mustParseURL(t, server.URL+"/.well-known/oauth-protected-resource")
	bundleGroupID := oauthBundleGroupID(resource, []string{issuer}, sourceURL)
	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		server.Client(),
		payload,
		time.Unix(42, 0).UTC(),
		sourceURL,
		URLBundleOptions{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("build expanded bundle: %v", err)
	}

	if len(bundle.URLs) != 10 {
		t.Fatalf("expected 10 urls, got %d", len(bundle.URLs))
	}

	assertURLRecord(t, bundle.URLs[0], resource, "prmd-resource", "0")
	assertURLRecord(t, bundle.URLs[1], issuer, "prmd-auth-server", "0")
	if got := tagValueForTest(bundle.URLs[1].Tags, hostbus.TagKeyGroup); got != "auth-server:"+bundleGroupID+":0" {
		t.Fatalf("unexpected group tag for prmd auth server: got %q", got)
	}
	assertURLRecord(t, bundle.URLs[2], sourceURL.String(), "prmd-source", "0")
	assertURLRecord(
		t,
		bundle.URLs[3],
		server.URL+"/.well-known/oauth-authorization-server/issuer-a",
		"auth-server-metadata",
		"0",
	)
	if got := tagValueForTest(bundle.URLs[3].Tags, hostbus.TagKeyGroup); got != "auth-server:"+bundleGroupID+":0" {
		t.Fatalf("unexpected group tag for auth-server metadata: got %q", got)
	}
	assertURLRecord(t, bundle.URLs[4], issuer, "issuer", "0")
	assertURLRecord(t, bundle.URLs[5], issuer+"/token", "token-endpoint", "0")
	assertURLRecord(t, bundle.URLs[6], issuer+"/jwks", "jwks-uri", "0")
	assertURLRecord(t, bundle.URLs[7], issuer+"/introspect", "introspection-endpoint", "0")
	assertURLRecord(t, bundle.URLs[8], issuer+"/register", "registration-endpoint", "0")
	assertURLRecord(t, bundle.URLs[9], issuer+"/revoke", "revocation-endpoint", "0")
	for _, record := range bundle.URLs {
		if record.UnixSocketPath != "" {
			t.Fatalf("expected HTTP-discovered record %q to keep empty unix socket path, got %q", tagValueForTest(record.Tags, hostbus.TagKeyRole), record.UnixSocketPath)
		}
		if record.DisallowPrivateHostRegistration {
			t.Fatalf("expected same-origin record %q to remain private-host eligible", tagValueForTest(record.Tags, hostbus.TagKeyRole))
		}
	}
}

func TestAppendAuthServerMetadataRecordUsesCanonicalOrigin(t *testing.T) {
	tests := []struct {
		name     string
		issuer   string
		record   string
		disallow bool
	}{
		{
			name:     "normalized host and default HTTPS port",
			issuer:   "https://Auth.Example.COM./issuer",
			record:   "https://auth.example.com:443/token",
			disallow: false,
		},
		{
			name:     "normalized explicit port",
			issuer:   "https://auth.example.com:08443/issuer",
			record:   "https://AUTH.EXAMPLE.COM.:8443/token",
			disallow: false,
		},
		{
			name:     "canonical IPv6 host",
			issuer:   "https://[::1]/issuer",
			record:   "https://[0:0:0:0:0:0:0:1]:443/token",
			disallow: false,
		},
		{
			name:     "different port",
			issuer:   "https://auth.example.com/issuer",
			record:   "https://auth.example.com:8443/token",
			disallow: true,
		},
		{
			name:     "different scheme",
			issuer:   "https://auth.example.com/issuer",
			record:   "http://auth.example.com/token",
			disallow: true,
		},
		{
			name:     "different host",
			issuer:   "https://auth.example.com/issuer",
			record:   "https://127.0.0.1/token",
			disallow: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			records := appendAuthServerMetadataRecord(
				nil,
				tc.record,
				"Auth server token endpoint",
				"token-endpoint",
				0,
				"bundle",
				mustParseURL(t, tc.issuer),
				true,
				URLBundleOptions{},
			)
			if len(records) != 1 {
				t.Fatalf("expected one record, got %d", len(records))
			}
			if got := records[0].DisallowPrivateHostRegistration; got != tc.disallow {
				t.Fatalf("unexpected private-host registration policy: got %t want %t", got, tc.disallow)
			}
		})
	}
}

func TestBuildURLBundleUsesTrustedMCPOriginForPrivateRegistration(t *testing.T) {
	const (
		trustedMCPURL = "https://mcp.example/mcp"
		privateOrigin = "https://10.0.0.1"
		sourceURL     = privateOrigin + "/.well-known/oauth-protected-resource"
		authServerURL = privateOrigin + "/oauth"
	)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(bytes.NewBufferString(
				"{\"issuer\":\"" + authServerURL + "\",\"token_endpoint\":\"" + privateOrigin + "/token\"}",
			)),
			Request: req,
		}, nil
	})}

	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		client,
		[]byte("{\"resource\":\""+privateOrigin+"/mcp\",\"authorization_servers\":[\""+authServerURL+"\"]}"),
		time.Unix(42, 0).UTC(),
		mustParseURL(t, sourceURL),
		URLBundleOptions{TrustedMCPURL: mustParseURL(t, trustedMCPURL)},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}

	recordsByRole := make(map[string]hostbus.URLRecord, len(bundle.URLs))
	for _, record := range bundle.URLs {
		recordsByRole[tagValueForTest(record.Tags, hostbus.TagKeyRole)] = record
	}
	for _, role := range []string{
		"prmd-source",
		"prmd-resource",
		"prmd-auth-server",
		"auth-server-metadata",
		"issuer",
		"token-endpoint",
	} {
		record, ok := recordsByRole[role]
		if !ok {
			t.Fatalf("missing %q record", role)
		}
		if !record.DisallowPrivateHostRegistration {
			t.Fatalf("expected off-origin %q record to disallow private-host registration", role)
		}
	}
}

func shortUnixSocketListener(t *testing.T) (net.Listener, string) {
	t.Helper()

	socketFile, err := os.CreateTemp("/tmp", "tunnel-oauth-*.sock")
	if err != nil {
		t.Fatalf("create unix socket path: %v", err)
	}
	socketPath := socketFile.Name()
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	if err := socketFile.Close(); err != nil {
		t.Fatalf("close unix socket placeholder: %v", err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatalf("remove unix socket placeholder: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}
	return listener, socketPath
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadataOverUnixSocket(t *testing.T) {
	t.Parallel()

	listener, socketPath := shortUnixSocketListener(t)

	const (
		logicalBaseURL = "http://localhost"
		variant        = "dcr10"
	)
	const (
		resourceURL     = logicalBaseURL + "/mcp/" + variant
		prmdURL         = logicalBaseURL + "/.well-known/oauth-protected-resource/mcp/" + variant
		authServerURL   = logicalBaseURL + "/" + variant
		authMetadataURL = logicalBaseURL + "/.well-known/oauth-authorization-server/" + variant
	)
	const externalIssuer = "https://external-idp.example.com/oauth2/aus2jrb9zi4O8hseE0h8"

	requestPaths := make(chan string, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/"+variant, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "localhost" {
			t.Fatalf("unexpected logical host: got %q", r.Host)
		}
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+prmdURL+`", scope="mcp:tools"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp/"+variant, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "localhost" {
			t.Fatalf("unexpected logical host: got %q", r.Host)
		}
		requestPaths <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		requireJSONWrite(t, w, oauthex.ProtectedResourceMetadata{
			Resource:             resourceURL,
			AuthorizationServers: []string{authServerURL},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/"+variant, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "localhost" {
			t.Fatalf("unexpected logical host: got %q", r.Host)
		}
		requestPaths <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		requireJSONWrite(t, w, map[string]any{
			"issuer":                 externalIssuer,
			"authorization_endpoint": authServerURL + "/authorize",
			"token_endpoint":         authServerURL + "/token",
			"registration_endpoint":  authServerURL + "/register",
			"revocation_endpoint":    authServerURL + "/revoke",
		})
	})

	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	baseTransport, err := transport.ApplyUnixSocketPath(http.DefaultTransport, socketPath)
	if err != nil {
		t.Fatalf("build unix transport: %v", err)
	}
	client := &http.Client{Transport: baseTransport}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	serverURL := mustParseURL(t, resourceURL)

	candidates, _, err := BuildOAuthDiscoveryCandidates(context.Background(), client, serverURL, logger)
	if err != nil {
		t.Fatalf("build discovery candidates: %v", err)
	}
	resp, sourceURL, _, err := FetchOAuthMetadata(context.Background(), client, candidates, logger)
	if err != nil {
		t.Fatalf("fetch PRMD over unix socket: %v", err)
	}
	if sourceURL == nil || sourceURL.String() != prmdURL {
		t.Fatalf("unexpected PRMD source URL: got %v want %q", sourceURL, prmdURL)
	}

	bundle, fetchResult, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		client,
		resp.Payload(),
		time.Unix(42, 0).UTC(),
		sourceURL,
		URLBundleOptions{
			UnixSocketPath: socketPath,
			UnixSocketURL:  serverURL,
		},
		logger,
	)
	if err != nil {
		t.Fatalf("build unix socket bundle: %v", err)
	}
	if fetchResult == nil || fetchResult.SelectedURL != authMetadataURL {
		t.Fatalf("unexpected auth metadata fetch result: %+v", fetchResult)
	}

	seenPaths := map[string]bool{}
	for len(requestPaths) > 0 {
		seenPaths[<-requestPaths] = true
	}
	for _, expectedPath := range []string{
		"/.well-known/oauth-protected-resource/mcp/" + variant,
		"/.well-known/oauth-authorization-server/" + variant,
	} {
		if !seenPaths[expectedPath] {
			t.Fatalf("expected unix socket request for %q, saw %v", expectedPath, seenPaths)
		}
	}
	for _, role := range []string{"auth-server-metadata", "registration-endpoint", "token-endpoint"} {
		assertURLRecordUnixSocket(t, bundle.URLs, role, socketPath)
	}
	assertURLRecordNoUnixSocket(t, bundle.URLs, "issuer")
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadataLimitsUnixSocketToAuthServerPath(t *testing.T) {
	t.Parallel()

	listener, socketPath := shortUnixSocketListener(t)

	const (
		logicalBaseURL = "http://localhost"
		variant        = "dcr10"
		resourceURL    = logicalBaseURL + "/mcp/" + variant
		prmdURL        = logicalBaseURL + "/.well-known/oauth-protected-resource/mcp/" + variant
		authServerURL  = logicalBaseURL + "/" + variant
	)

	mux := http.NewServeMux()
	for _, metadataPath := range []string{
		"/" + variant + "/.well-known/oauth-authorization-server",
		"/.well-known/oauth-authorization-server/" + variant,
	} {
		mux.HandleFunc(metadataPath, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			requireJSONWrite(t, w, map[string]any{
				"issuer":                authServerURL,
				"token_endpoint":        logicalBaseURL + "/admin/token",
				"registration_endpoint": authServerURL + "/register",
				"revocation_endpoint":   logicalBaseURL + "/admin/revoke",
			})
		})
	}

	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	baseTransport, err := transport.ApplyUnixSocketPath(http.DefaultTransport, socketPath)
	if err != nil {
		t.Fatalf("build unix transport: %v", err)
	}
	serverURL := mustParseURL(t, resourceURL)
	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		&http.Client{Transport: baseTransport},
		[]byte(`{"resource":"`+resourceURL+`","authorization_servers":["`+authServerURL+`"]}`),
		time.Unix(42, 0).UTC(),
		mustParseURL(t, prmdURL),
		URLBundleOptions{
			UnixSocketPath: socketPath,
			UnixSocketURL:  serverURL,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("build unix socket bundle: %v", err)
	}

	assertURLRecordUnixSocket(t, bundle.URLs, "auth-server-metadata", socketPath)
	assertURLRecordUnixSocket(t, bundle.URLs, "registration-endpoint", socketPath)
	assertURLRecordNoUnixSocket(t, bundle.URLs, "token-endpoint")
	assertURLRecordNoUnixSocket(t, bundle.URLs, "revocation-endpoint")
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadataDoesNotPropagateUnixSocketAcrossOrigins(t *testing.T) {
	t.Parallel()

	listener, socketPath := shortUnixSocketListener(t)

	const (
		logicalBaseURL = "http://localhost"
		variant        = "dcr10"
		resourceURL    = logicalBaseURL + "/mcp/" + variant
		prmdURL        = logicalBaseURL + "/.well-known/oauth-protected-resource/mcp/" + variant
		authServerURL  = logicalBaseURL + "/" + variant
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server/"+variant, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requireJSONWrite(t, w, map[string]any{
			"issuer":                "http://private-idp.example.com/issuer",
			"token_endpoint":        "http://private-idp.example.com/token",
			"jwks_uri":              "http://private-idp.example.com/jwks",
			"registration_endpoint": "http://private-idp.example.com/register",
		})
	})

	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	baseTransport, err := transport.ApplyUnixSocketPath(http.DefaultTransport, socketPath)
	if err != nil {
		t.Fatalf("build unix transport: %v", err)
	}
	serverURL := mustParseURL(t, resourceURL)
	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		&http.Client{Transport: baseTransport},
		[]byte(`{"resource":"`+resourceURL+`","authorization_servers":["`+authServerURL+`"]}`),
		time.Unix(42, 0).UTC(),
		mustParseURL(t, prmdURL),
		URLBundleOptions{
			UnixSocketPath: socketPath,
			UnixSocketURL:  serverURL,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("build unix socket bundle: %v", err)
	}

	assertURLRecordUnixSocket(t, bundle.URLs, "auth-server-metadata", socketPath)
	for _, role := range []string{"issuer", "token-endpoint", "jwks-uri", "registration-endpoint"} {
		assertURLRecordNoUnixSocket(t, bundle.URLs, role)
	}
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadataAcceptsIssuerMismatch(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	authServerURL := server.URL + "/issuer-a"
	externalIssuer := "https://userinfo-value:credential-value@idp.bigco-example.com/token-path-value?client_id=query-value#state=fragment-value"
	registrationEndpoint := "https://location-mcp.internal.preproduction.smp.bigco-example.com/register"
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource:             server.URL + "/resource",
		AuthorizationServers: []string{authServerURL},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	metaBody, err := json.Marshal(map[string]any{
		"issuer":                 externalIssuer,
		"authorization_endpoint": externalIssuer + "/authorize",
		"token_endpoint":         externalIssuer + "/token",
		"jwks_uri":               externalIssuer + "/jwks",
		"introspection_endpoint": externalIssuer + "/introspect",
		"registration_endpoint":  registrationEndpoint,
		"revocation_endpoint":    externalIssuer + "/revoke",
	})
	if err != nil {
		t.Fatalf("marshal metadata body: %v", err)
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer-a", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(metaBody)
	})

	var logBuffer bytes.Buffer
	bundle, fetchResult, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		server.Client(),
		payload,
		time.Unix(42, 0).UTC(),
		mustParseURL(t, server.URL+"/.well-known/oauth-protected-resource"),
		URLBundleOptions{},
		slog.New(slog.NewJSONHandler(&logBuffer, nil)),
	)
	if err != nil {
		t.Fatalf("build expanded bundle: %v", err)
	}
	if fetchResult == nil {
		t.Fatalf("expected auth-server metadata fetch result")
		return
	}
	result := fetchResult
	if len(bundle.URLs) != 10 {
		t.Fatalf("expected 10 urls, got %d", len(bundle.URLs))
	}

	urlByRole := map[string]string{}
	recordByRole := map[string]hostbus.URLRecord{}
	for _, record := range bundle.URLs {
		if record.URL == nil {
			t.Fatalf("expected URL for role %q", tagValueForTest(record.Tags, hostbus.TagKeyRole))
			return
		}
		role := tagValueForTest(record.Tags, hostbus.TagKeyRole)
		urlByRole[role] = record.URL.String()
		recordByRole[role] = record
	}
	if got := urlByRole["auth-server-metadata"]; got != server.URL+"/.well-known/oauth-authorization-server/issuer-a" {
		t.Fatalf("unexpected auth-server-metadata url: got %q", got)
	}
	if got := urlByRole["issuer"]; got != externalIssuer {
		t.Fatalf("unexpected issuer url: got %q want %q", got, externalIssuer)
	}
	if got := urlByRole["token-endpoint"]; got != externalIssuer+"/token" {
		t.Fatalf("unexpected token endpoint url: got %q", got)
	}
	if got := urlByRole["jwks-uri"]; got != externalIssuer+"/jwks" {
		t.Fatalf("unexpected jwks uri: got %q", got)
	}
	if got := urlByRole["introspection-endpoint"]; got != externalIssuer+"/introspect" {
		t.Fatalf("unexpected introspection endpoint: got %q", got)
	}
	if got := urlByRole["registration-endpoint"]; got != registrationEndpoint {
		t.Fatalf("unexpected registration endpoint: got %q", got)
	}
	if got := urlByRole["revocation-endpoint"]; got != externalIssuer+"/revoke" {
		t.Fatalf("unexpected revocation endpoint: got %q", got)
	}
	if recordByRole["auth-server-metadata"].DisallowPrivateHostRegistration {
		t.Fatalf("expected advertised auth-server metadata URL to remain private-host eligible")
	}
	for _, role := range []string{
		"issuer",
		"token-endpoint",
		"jwks-uri",
		"introspection-endpoint",
		"registration-endpoint",
		"revocation-endpoint",
	} {
		if !recordByRole[role].DisallowPrivateHostRegistration {
			t.Fatalf("expected off-origin %q record to disallow classifier-only private registration", role)
		}
	}

	if result.SelectedURL != server.URL+"/.well-known/oauth-authorization-server/issuer-a" {
		t.Fatalf("unexpected selected metadata URL: got %q", result.SelectedURL)
	}
	if len(result.Attempts) == 0 {
		t.Fatalf("expected auth-server metadata attempts")
	}
	selected := selectedAuthServerMetadataAttempt(t, result.Attempts)
	if !selected.IssuerMismatch {
		t.Fatalf("expected issuer mismatch diagnostics on selected metadata attempt")
	}
	if selected.ExpectedIssuerURL != authServerURL {
		t.Fatalf(
			"unexpected expected issuer URL diagnostic: got %q want %q",
			selected.ExpectedIssuerURL,
			authServerURL,
		)
	}
	if selected.MetadataIssuer != externalIssuer {
		t.Fatalf("unexpected metadata issuer diagnostic: got %q want %q", selected.MetadataIssuer, externalIssuer)
	}
	if selected.Error != "" {
		t.Fatalf("did not expect hard error for issuer mismatch, got %q", selected.Error)
	}

	var logRecord map[string]any
	if err := json.NewDecoder(&logBuffer).Decode(&logRecord); err != nil {
		t.Fatalf("decode issuer-mismatch log: %v", err)
	}
	if got := logRecord["metadata_issuer"]; got != "https://idp.bigco-example.com" {
		t.Fatalf("unexpected redacted metadata issuer: got %v", got)
	}
	for _, sensitive := range []string{
		"userinfo-value",
		"credential-value",
		"token-path-value",
		"query-value",
		"fragment-value",
	} {
		if bytes.Contains(logBuffer.Bytes(), []byte(sensitive)) {
			t.Fatalf("issuer-mismatch log contains %q: %s", sensitive, logBuffer.String())
		}
	}
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadataPartialFailure(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuerA := server.URL + "/issuer-a"
	issuerB := server.URL + "/issuer-b"
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource:             server.URL + "/resource",
		AuthorizationServers: []string{issuerA, issuerB},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	metaBody, err := json.Marshal(map[string]any{
		"issuer":         issuerA,
		"token_endpoint": issuerA + "/token",
	})
	if err != nil {
		t.Fatalf("marshal metadata body: %v", err)
	}
	var issuerARequests int
	var issuerBRequests int
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer-a", func(w http.ResponseWriter, _ *http.Request) {
		issuerARequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(metaBody)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer-b", func(w http.ResponseWriter, _ *http.Request) {
		issuerBRequests++
		http.Error(w, "upstream error", http.StatusBadGateway)
	})

	sourceURL := mustParseURL(t, server.URL+"/.well-known/oauth-protected-resource")
	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		server.Client(),
		payload,
		time.Unix(42, 0).UTC(),
		sourceURL,
		URLBundleOptions{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("build expanded bundle: %v", err)
	}

	// Base PRMD records (resource + first auth-server + source) plus 3 metadata-derived records from issuer-a.
	if len(bundle.URLs) != 6 {
		t.Fatalf("expected 6 urls, got %d", len(bundle.URLs))
	}
	assertURLRecord(t, bundle.URLs[0], server.URL+"/resource", "prmd-resource", "0")
	assertURLRecord(t, bundle.URLs[1], issuerA, "prmd-auth-server", "0")
	assertURLRecord(t, bundle.URLs[2], sourceURL.String(), "prmd-source", "0")
	assertURLRecord(
		t,
		bundle.URLs[3],
		server.URL+"/.well-known/oauth-authorization-server/issuer-a",
		"auth-server-metadata",
		"0",
	)
	assertURLRecord(t, bundle.URLs[4], issuerA, "issuer", "0")
	assertURLRecord(t, bundle.URLs[5], issuerA+"/token", "token-endpoint", "0")
	if issuerARequests != 1 {
		t.Fatalf("expected exactly one metadata request for first auth server, got %d", issuerARequests)
	}
	if issuerBRequests != 0 {
		t.Fatalf("expected no metadata request for second auth server, got %d", issuerBRequests)
	}
}

func TestBuildURLBundleFromPRMDWithAuthServerMetadataUsesFirstAuthServerOnly(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	issuerA := server.URL + "/issuer-a"
	issuerB := server.URL + "/issuer-b"
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource:             server.URL + "/resource",
		AuthorizationServers: []string{issuerA, issuerB},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var issuerARequests int
	var issuerBRequests int
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer-a", func(w http.ResponseWriter, _ *http.Request) {
		issuerARequests++
		http.Error(w, "first issuer unavailable", http.StatusBadGateway)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer-b", func(w http.ResponseWriter, _ *http.Request) {
		issuerBRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + issuerB + `","token_endpoint":"` + issuerB + `/token"}`))
	})

	sourceURL := mustParseURL(t, server.URL+"/.well-known/oauth-protected-resource")
	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		server.Client(),
		payload,
		time.Unix(42, 0).UTC(),
		sourceURL,
		URLBundleOptions{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("build expanded bundle: %v", err)
	}

	// Base PRMD records (resource + first auth-server + source), no auth-metadata-derived records.
	if len(bundle.URLs) != 3 {
		t.Fatalf("expected 3 urls, got %d", len(bundle.URLs))
	}
	if issuerARequests != 1 {
		t.Fatalf("expected exactly one metadata request for first auth server, got %d", issuerARequests)
	}
	if issuerBRequests != 0 {
		t.Fatalf("expected no metadata request for second auth server, got %d", issuerBRequests)
	}
}

func TestBuildURLBundleFromPRMDIgnoresAuthorizationServersBeyondIndexZero(t *testing.T) {
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource:             "https://resource.internal/",
		AuthorizationServers: []string{"https://auth1.internal/", "://not-a-url"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	bundle, _, err := buildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		nil,
		payload,
		time.Unix(42, 0).UTC(),
		mustParseURL(t, "https://prmd.internal/.well-known/oauth-protected-resource"),
		URLBundleOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if len(bundle.URLs) != 3 {
		t.Fatalf("expected 3 urls, got %d", len(bundle.URLs))
	}
	assertURLRecord(t, bundle.URLs[0], "https://resource.internal/", "prmd-resource", "0")
	assertURLRecord(t, bundle.URLs[1], "https://auth1.internal/", "prmd-auth-server", "0")
	assertURLRecord(t, bundle.URLs[2], "https://prmd.internal/.well-known/oauth-protected-resource", "prmd-source", "0")
}

func TestOAuthBundleGroupIDUsesSourceURLWhenAvailable(t *testing.T) {
	sourceA := mustParseURL(t, "https://bundle-a.internal/.well-known/oauth-protected-resource")
	sourceB := mustParseURL(t, "https://bundle-b.internal/.well-known/oauth-protected-resource")

	groupA := authServerGroup(oauthBundleGroupID("https://resource.internal/", []string{"https://auth.internal/"}, sourceA), 0)
	groupB := authServerGroup(oauthBundleGroupID("https://resource.internal/", []string{"https://auth.internal/"}, sourceB), 0)
	if groupA == groupB {
		t.Fatalf("expected distinct auth-server groups for different source urls, got %q", groupA)
	}
}

func assertURLRecord(t *testing.T, record hostbus.URLRecord, expectedURL string, expectedRole string, expectedIndex string) {
	t.Helper()
	if record.URL == nil {
		t.Fatalf("expected URL %q, got nil", expectedURL)
	}
	if got := record.URL.String(); got != expectedURL {
		t.Fatalf("unexpected url: got %q want %q", got, expectedURL)
	}
	if got := tagValueForTest(record.Tags, hostbus.TagKeyRole); got != expectedRole {
		t.Fatalf("unexpected role: got %q want %q", got, expectedRole)
	}
	if got := tagValueForTest(record.Tags, hostbus.TagKeyIndex); got != expectedIndex {
		t.Fatalf("unexpected index: got %q want %q", got, expectedIndex)
	}
}

func assertURLRecordUnixSocket(t *testing.T, records []hostbus.URLRecord, role string, socketPath string) {
	t.Helper()
	for _, record := range records {
		if tagValueForTest(record.Tags, hostbus.TagKeyRole) != role {
			continue
		}
		if record.UnixSocketPath != socketPath {
			t.Fatalf("unexpected unix socket path for role %q: got %q want %q", role, record.UnixSocketPath, socketPath)
		}
		return
	}
	t.Fatalf("expected URL record for role %q", role)
}

func assertURLRecordNoUnixSocket(t *testing.T, records []hostbus.URLRecord, role string) {
	t.Helper()
	for _, record := range records {
		if tagValueForTest(record.Tags, hostbus.TagKeyRole) != role {
			continue
		}
		if record.UnixSocketPath != "" {
			t.Fatalf("unexpected unix socket path for role %q: got %q", role, record.UnixSocketPath)
		}
		return
	}
	t.Fatalf("expected URL record for role %q", role)
}

func requireJSONWrite(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode JSON response: %v", err)
	}
}

func tagValueForTest(tags []hostbus.Tag, key hostbus.TagKey) string {
	for _, tag := range tags {
		if tag.Key == key {
			return tag.Value
		}
	}
	return ""
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}

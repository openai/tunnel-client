package log

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestRedactURLKeepsOnlyOriginWithoutMutation(t *testing.T) {
	sensitiveURL := &url.URL{
		Scheme:      "https",
		Opaque:      "//opaque-user:opaque-credential@opaque.internal/token-path?opaque-query#opaque-fragment",
		User:        url.UserPassword("client", "credential"),
		Host:        "auth.internal:8443",
		Path:        "/oauth/token-path",
		RawPath:     "/oauth/%74oken-path",
		ForceQuery:  true,
		RawQuery:    "client_id=identifier",
		Fragment:    "state=fragment-value",
		RawFragment: "state%3Dfragment-value",
	}
	original := *sensitiveURL

	if got := RedactURL(sensitiveURL); got != "https://auth.internal:8443" {
		t.Fatalf("unexpected redacted url: got %q", got)
	}
	if !reflect.DeepEqual(*sensitiveURL, original) {
		t.Fatalf("source url was mutated: got %#v want %#v", *sensitiveURL, original)
	}
	if got := RedactURL(nil); got != "" {
		t.Fatalf("unexpected nil url: got %q", got)
	}
	if got := RedactURL(&url.URL{Path: "/relative"}); got != "" {
		t.Fatalf("unexpected relative url: got %q", got)
	}
}

func TestErrorForLogRedactsNestedURLError(t *testing.T) {
	rawURL := "https://url-user-secret:url-password-secret@auth.internal/url-path-secret?url-query-key=url-query-secret#url-fragment-secret"
	urlErr := &url.Error{
		Op:  "Get",
		URL: rawURL,
		Err: errors.New("connection refused"),
	}
	err := fmt.Errorf("fetch metadata: %w", errors.Join(urlErr, errors.New("second candidate failed")))
	original := err.Error()

	got := ErrorForLog(err)
	if !strings.Contains(got, "fetch metadata:") {
		t.Fatalf("redacted error lost wrapper context: %q", got)
	}
	if !strings.Contains(got, "second candidate failed") {
		t.Fatalf("redacted error lost joined sibling: %q", got)
	}
	if !strings.Contains(got, "Get") || !strings.Contains(got, "connection refused") {
		t.Fatalf("redacted error lost URL operation or underlying failure: %q", got)
	}
	if !strings.Contains(got, "https://auth.internal") {
		t.Fatalf("redacted error is missing origin: %q", got)
	}
	for _, sensitive := range []string{
		"url-user-secret",
		"url-password-secret",
		"url-path-secret",
		"url-query-key",
		"url-query-secret",
		"url-fragment-secret",
	} {
		if strings.Contains(got, sensitive) {
			t.Fatalf("redacted error contains %q: %q", sensitive, got)
		}
	}
	if err.Error() != original {
		t.Fatalf("source error was mutated: got %q want %q", err.Error(), original)
	}
}

func TestErrorForLogHandlesEmptyURLErrorURL(t *testing.T) {
	err := &url.Error{Op: "Get", Err: errors.New("connection refused")}
	got := ErrorForLog(err)
	if got != err.Error() {
		t.Fatalf("empty URL error was corrupted: got %q want %q", got, err.Error())
	}
}

func TestErrorForLogDoesNotReplaceRelativeURLCharactersInContext(t *testing.T) {
	err := fmt.Errorf("metadata wrapper: %w", &url.Error{
		Op:  "Get",
		URL: "a",
		Err: errors.New("candidate available"),
	})
	const want = `metadata wrapper: Get "<redacted>": candidate available`
	if got := ErrorForLog(err); got != want {
		t.Fatalf("short relative URL corrupted diagnostic: got %q want %q", got, want)
	}
}

func TestErrorForLogPreservesNonURLQuotedParseDiagnostic(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://auth.internal/sensitive-path",
		Err: errors.New(`failed to parse "response-shape": invalid`),
	}
	got := ErrorForLog(err)
	if !strings.Contains(got, `failed to parse "response-shape": invalid`) {
		t.Fatalf("redacted error lost non-URL parse diagnostic: %q", got)
	}
}

func TestErrorForLogRedactsQuotedInvalidURL(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://control-user-secret:control-password-secret@auth.internal/control-path-secret\n?control-query-secret=value",
		Err: errors.New("invalid control character in URL"),
	}
	got := ErrorForLog(err)
	for _, sensitive := range []string{
		"control-user-secret",
		"control-password-secret",
		"control-path-secret",
		"control-query-secret",
	} {
		if strings.Contains(got, sensitive) {
			t.Fatalf("redacted error contains invalid URL value %q: %q", sensitive, got)
		}
	}
	if !strings.Contains(got, "invalid control character in URL") {
		t.Fatalf("redacted error lost underlying failure: %q", got)
	}
}

func TestErrorForLogRedactsURLDerivedParseDetails(t *testing.T) {
	testCases := []struct {
		name      string
		rawURL    string
		sensitive []string
	}{
		{
			name:   "invalid port",
			rawURL: "https://parse-user-secret:parse-password-secret@auth.internal:parse-port-secret/parse-path-secret?parse-query-secret=value#parse-fragment-secret",
			sensitive: []string{
				"parse-user-secret",
				"parse-password-secret",
				"parse-port-secret",
				"parse-path-secret",
				"parse-query-secret",
				"parse-fragment-secret",
			},
		},
		{
			name:   "invalid escape",
			rawURL: "https://escape-user-secret:escape-password-secret@auth.internal/escape-path-secret%zz?escape-query-secret=value#escape-fragment-secret",
			sensitive: []string{
				"escape-user-secret",
				"escape-password-secret",
				"escape-path-secret",
				"%zz",
				"escape-query-secret",
				"escape-fragment-secret",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := url.Parse(tc.rawURL)
			if err == nil {
				t.Fatal("expected URL parse error")
			}
			if !strings.Contains(err.Error(), tc.sensitive[2]) {
				t.Fatalf("test did not exercise URL-derived parse detail: %v", err)
			}
			got := ErrorForLog(err)
			if !strings.Contains(got, "parse") || !strings.Contains(got, "invalid URL") {
				t.Fatalf("redacted error lost generic parse diagnostic: %q", got)
			}
			for _, sensitive := range tc.sensitive {
				if strings.Contains(got, sensitive) {
					t.Fatalf("redacted error contains parse value %q: %q", sensitive, got)
				}
			}
		})
	}
}

func TestErrorForLogRedactsMalformedRedirectLocation(t *testing.T) {
	const location = "https://redirect-user-secret:redirect-password-secret@redirect.internal:redirect-port-secret/redirect-path-secret?redirect-query=secret#redirect-fragment-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)

	_, err := server.Client().Get(server.URL)
	if err == nil {
		t.Fatal("expected malformed redirect error")
	}
	if !strings.Contains(err.Error(), "failed to parse Location header") || !strings.Contains(err.Error(), "redirect-port-secret") {
		t.Fatalf("test did not exercise sensitive malformed Location diagnostic: %v", err)
	}
	got := ErrorForLog(err)
	if !strings.Contains(got, "failed to parse Location header") {
		t.Fatalf("redacted error lost redirect diagnostic: %q", got)
	}
	for _, sensitive := range []string{
		"redirect-user-secret",
		"redirect-password-secret",
		"redirect-port-secret",
		"redirect-path-secret",
		"redirect-query",
		"redirect-fragment-secret",
	} {
		if strings.Contains(got, sensitive) {
			t.Fatalf("redacted error contains redirect value %q: %q", sensitive, got)
		}
	}
	if !strings.Contains(got, "invalid URL") {
		t.Fatalf("redacted error lost generic redirect diagnostic: %q", got)
	}
}

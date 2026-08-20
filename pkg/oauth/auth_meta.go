package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	tclog "github.com/openai/tunnel-client/pkg/log"
)

// TODO: Replace this local implementation with oauthex.GetAuthServerMeta once
// the upstream mcp_go_client_oauth build-tag gate is removed.

// AuthServerMetadata is a narrow projection used by tunnel-client OAuth URL collection.
type AuthServerMetadata struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURI               string
	IntrospectionEndpoint string
	RegistrationEndpoint  string
	RevocationEndpoint    string
}

type AuthServerMetadataDocument string

const (
	AuthServerMetadataDocumentOAuthAuthorizationServer AuthServerMetadataDocument = "oauth-authorization-server"
	AuthServerMetadataDocumentOpenIDConfiguration      AuthServerMetadataDocument = "openid-configuration"
)

type AuthServerMetadataPathStyle string

const (
	AuthServerMetadataPathStylePrepend AuthServerMetadataPathStyle = "prepend"
	AuthServerMetadataPathStyleAppend  AuthServerMetadataPathStyle = "append"
)

type AuthServerMetadataAttempt struct {
	URL               string                      `json:"url,omitempty"`
	Document          AuthServerMetadataDocument  `json:"document,omitempty"`
	PathStyle         AuthServerMetadataPathStyle `json:"path_style,omitempty"`
	Tried             bool                        `json:"tried,omitempty"`
	Selected          bool                        `json:"selected,omitempty"`
	StatusCode        int                         `json:"status_code,omitempty"`
	Error             string                      `json:"error,omitempty"`
	Warning           string                      `json:"warning,omitempty"`
	IssuerMismatch    bool                        `json:"issuer_mismatch,omitempty"`
	ExpectedIssuerURL string                      `json:"expected_issuer_url,omitempty"`
	MetadataIssuer    string                      `json:"metadata_issuer,omitempty"`
	Headers           http.Header                 `json:"headers,omitempty"`
	Body              json.RawMessage             `json:"body,omitempty"`
	BodyText          string                      `json:"body_text,omitempty"`
}

type AuthServerMetadataFetchResult struct {
	IssuerURL   string                      `json:"issuer_url,omitempty"`
	SelectedURL string                      `json:"selected_url,omitempty"`
	Attempts    []AuthServerMetadataAttempt `json:"attempts,omitempty"`
}

type authServerWellKnownPathSpec struct {
	Path     string
	Document AuthServerMetadataDocument
}

var authServerWellKnownPathSpecs = []authServerWellKnownPathSpec{
	{Path: "/.well-known/oauth-authorization-server", Document: AuthServerMetadataDocumentOAuthAuthorizationServer},
	{Path: "/.well-known/openid-configuration", Document: AuthServerMetadataDocumentOpenIDConfiguration},
}

type authServerMetadataJSON struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	IntrospectionEndpoint string `json:"introspection_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

type authServerMetadataCandidate struct {
	URL       string
	PathStyle AuthServerMetadataPathStyle
	Document  AuthServerMetadataDocument
}

// FetchAuthServerMetadata fetches RFC 8414 authorization server metadata for issuerURL.
func FetchAuthServerMetadata(ctx context.Context, client *http.Client, issuerURL string) (*AuthServerMetadata, error) {
	meta, _, err := FetchAuthServerMetadataWithResult(ctx, client, issuerURL)
	return meta, err
}

// FetchAuthServerMetadataWithResult fetches metadata and captures detailed fetch attempts.
func FetchAuthServerMetadataWithResult(
	ctx context.Context,
	client *http.Client,
	issuerURL string,
) (*AuthServerMetadata, *AuthServerMetadataFetchResult, error) {
	if client == nil {
		client = http.DefaultClient
	}

	candidates, err := buildAuthServerMetadataCandidateURLs(issuerURL)
	if err != nil {
		return nil, nil, wrapErrorWithRedactedMessage(
			fmt.Sprintf("oauth auth-server metadata: invalid issuer URL: %s", tclog.ErrorForLog(err)),
			err,
		)
	}

	result := &AuthServerMetadataFetchResult{
		IssuerURL: issuerURL,
		Attempts:  make([]AuthServerMetadataAttempt, 0, len(candidates)),
	}

	meta, errs, failureType := runFetchAuthServerMetadataPass(ctx, client, issuerURL, candidates, result, discoveryRetryModeNone)
	if meta != nil {
		return meta, result, nil
	}

	if failureType == discoveryFailureTypeTimeoutOnly {
		result.Attempts = result.Attempts[:0]
		meta, errs, _ = runFetchAuthServerMetadataPass(ctx, client, issuerURL, candidates, result, discoveryRetryModeTimeoutBackoff)
		if meta != nil {
			return meta, result, nil
		}
	}

	return nil, result, fmt.Errorf(
		"oauth auth-server metadata fetch failed for issuer %q: %w",
		tclog.RedactURLString(issuerURL),
		errors.Join(errs...),
	)
}

func runFetchAuthServerMetadataPass(
	ctx context.Context,
	client *http.Client,
	issuerURL string,
	candidates []authServerMetadataCandidate,
	result *AuthServerMetadataFetchResult,
	retryMode discoveryRetryMode,
) (*AuthServerMetadata, []error, discoveryFailureType) {
	var errs []error
	failureType := discoveryFailureTypeTimeoutOnly
	var fallbackMeta *AuthServerMetadata
	fallbackAttemptIndex := -1
	fallbackSelectedURL := ""

	for _, candidate := range candidates {
		attempt := AuthServerMetadataAttempt{
			URL:       candidate.URL,
			Document:  candidate.Document,
			PathStyle: candidate.PathStyle,
			Tried:     true,
		}

		meta, response, err := fetchAuthServerMetadataDocument(ctx, client, candidate.URL, retryMode)
		if response != nil {
			attempt.StatusCode = response.StatusCode
			attempt.Headers = response.Headers
			attempt.Body = response.Body
			attempt.BodyText = response.BodyText
		}
		if err != nil {
			attempt.Error = tclog.ErrorForLog(err)
			result.Attempts = append(result.Attempts, attempt)
			errs = append(errs, wrapErrorWithRedactedMessage(
				fmt.Sprintf("%s: %s", tclog.RedactURLString(candidate.URL), tclog.ErrorForLog(err)),
				err,
			))
			if classifyDiscoveryFailure(err) == discoveryFailureTypeNonTimeout {
				failureType = discoveryFailureTypeNonTimeout
			}
			continue
		}

		if meta.Issuer != issuerURL {
			attempt.IssuerMismatch = true
			attempt.ExpectedIssuerURL = issuerURL
			attempt.MetadataIssuer = meta.Issuer
			attempt.Warning = fmt.Sprintf("issuer mismatch: got %q want %q", meta.Issuer, issuerURL)
			if fallbackMeta == nil {
				fallbackMeta = meta
				fallbackAttemptIndex = len(result.Attempts)
				fallbackSelectedURL = candidate.URL
			}
			result.Attempts = append(result.Attempts, attempt)
			continue
		}

		attempt.Selected = true
		result.SelectedURL = candidate.URL
		result.Attempts = append(result.Attempts, attempt)
		return meta, errs, discoveryFailureTypeNotApplicable
	}

	if fallbackMeta != nil && fallbackAttemptIndex >= 0 {
		result.Attempts[fallbackAttemptIndex].Selected = true
		result.SelectedURL = fallbackSelectedURL
		return fallbackMeta, errs, discoveryFailureTypeNotApplicable
	}

	return nil, errs, failureType
}

func buildAuthServerMetadataCandidateURLs(issuerURL string) ([]authServerMetadataCandidate, error) {
	parsedIssuer, err := url.Parse(issuerURL)
	if err != nil {
		return nil, err
	}
	pathfulIssuer := strings.Trim(parsedIssuer.EscapedPath(), "/") != ""

	seen := make(map[string]struct{}, len(authServerWellKnownPathSpecs)*2)
	out := make([]authServerMetadataCandidate, 0, len(authServerWellKnownPathSpecs)*2)

	for _, spec := range authServerWellKnownPathSpecs {
		appendURL, err := appendToPath(issuerURL, spec.Path)
		if err != nil {
			return nil, err
		}
		prependURL, err := prependToPath(issuerURL, spec.Path)
		if err != nil {
			return nil, err
		}

		orderedCandidates := []authServerMetadataCandidate{
			{
				URL:       prependURL,
				PathStyle: AuthServerMetadataPathStylePrepend,
				Document:  spec.Document,
			},
			{
				URL:       appendURL,
				PathStyle: AuthServerMetadataPathStyleAppend,
				Document:  spec.Document,
			},
		}
		if pathfulIssuer {
			orderedCandidates[0], orderedCandidates[1] = orderedCandidates[1], orderedCandidates[0]
		}

		for _, candidate := range orderedCandidates {
			if _, ok := seen[candidate.URL]; ok {
				continue
			}
			seen[candidate.URL] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out, nil
}

type authServerMetadataHTTPResponse struct {
	StatusCode int
	Headers    http.Header
	Body       json.RawMessage
	BodyText   string
}

func fetchAuthServerMetadataDocument(
	ctx context.Context,
	client *http.Client,
	metadataURL string,
	retryMode discoveryRetryMode,
) (*AuthServerMetadata, *authServerMetadataHTTPResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, nil, err
	}
	redirectOrigin, err := url.Parse(metadataURL)
	if err != nil {
		return nil, nil, err
	}
	discoveryClient := withSameOriginRedirects(client, redirectOrigin)

	var res *http.Response
	if retryMode == discoveryRetryModeTimeoutBackoff {
		res, err = doWithRetryForTimeout(ctx, discoveryClient, req, nil)
	} else {
		res, err = discoveryClient.Do(req)
	}
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = res.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(io.LimitReader(res.Body, authServerMetadataBodyLimitBytes))
	if err != nil {
		return nil, &authServerMetadataHTTPResponse{
			StatusCode: res.StatusCode,
			Headers:    res.Header.Clone(),
		}, err
	}

	response := &authServerMetadataHTTPResponse{
		StatusCode: res.StatusCode,
		Headers:    res.Header.Clone(),
	}
	if json.Valid(bodyBytes) {
		response.Body = append(json.RawMessage(nil), bodyBytes...)
	} else if len(bodyBytes) > 0 {
		response.BodyText = string(bodyBytes)
	}

	if res.StatusCode != http.StatusOK {
		return nil, response, fmt.Errorf("bad status %s", res.Status)
	}

	contentType := res.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return nil, response, fmt.Errorf("bad content type %q", contentType)
	}

	var metadata authServerMetadataJSON
	if err := json.Unmarshal(bodyBytes, &metadata); err != nil {
		return nil, response, err
	}

	return &AuthServerMetadata{
		Issuer:                metadata.Issuer,
		AuthorizationEndpoint: metadata.AuthorizationEndpoint,
		TokenEndpoint:         metadata.TokenEndpoint,
		JWKSURI:               metadata.JWKSURI,
		IntrospectionEndpoint: metadata.IntrospectionEndpoint,
		RegistrationEndpoint:  metadata.RegistrationEndpoint,
		RevocationEndpoint:    metadata.RevocationEndpoint,
	}, response, nil
}

func prependToPath(urlStr, pre string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	pathPrefix := "/" + strings.Trim(pre, "/")
	if u.Path != "" {
		pathPrefix += "/"
	}

	u.Path = pathPrefix + strings.TrimLeft(u.Path, "/")
	return u.String(), nil
}

func appendToPath(urlStr, suffix string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	suffixPart := strings.Trim(suffix, "/")
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == "" {
		u.Path = "/" + suffixPart
	} else {
		u.Path = basePath + "/" + suffixPart
	}
	return u.String(), nil
}

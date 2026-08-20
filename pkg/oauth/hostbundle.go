package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

// URLBundleOptions carries optional transport hints and trust context for
// discovered URLs.
type URLBundleOptions struct {
	UnixSocketPath string
	UnixSocketURL  *url.URL
	TrustedMCPURL  *url.URL
}

func (o URLBundleOptions) apply(record hostbus.URLRecord) hostbus.URLRecord {
	unixSocketPath := strings.TrimSpace(o.UnixSocketPath)
	if unixSocketPath == "" || record.URL == nil || o.UnixSocketURL == nil {
		return record
	}
	if !sameURLOrigin(record.URL, o.UnixSocketURL) {
		return record
	}
	record.UnixSocketPath = unixSocketPath
	return record
}

func (o URLBundleOptions) applyAuthServerMetadata(record hostbus.URLRecord, issuerURL *url.URL) hostbus.URLRecord {
	unixSocketPath := strings.TrimSpace(o.UnixSocketPath)
	if unixSocketPath == "" || record.URL == nil || o.UnixSocketURL == nil || issuerURL == nil {
		return record
	}
	if !sameURLOrigin(record.URL, o.UnixSocketURL) || !sameURLOrigin(record.URL, issuerURL) {
		return record
	}
	if !sameOrChildURLPath(record.URL, issuerURL) && !isAuthServerMetadataURLFor(record.URL, issuerURL) {
		return record
	}
	record.UnixSocketPath = unixSocketPath
	return record
}

func sameURLOrigin(left *url.URL, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		normalizedURLHostname(left) == normalizedURLHostname(right) &&
		effectiveURLPort(left) == effectiveURLPort(right)
}

func normalizedURLHostname(raw *url.URL) string {
	if raw == nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw.Hostname())), ".")
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String()
	}
	return host
}

func effectiveURLPort(raw *url.URL) string {
	if raw == nil {
		return ""
	}
	if port := raw.Port(); port != "" {
		if parsedPort, err := strconv.Atoi(port); err == nil {
			return strconv.Itoa(parsedPort)
		}
		return port
	}
	switch strings.ToLower(raw.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func sameOrChildURLPath(candidate *url.URL, base *url.URL) bool {
	if candidate == nil || base == nil {
		return false
	}
	candidatePath := normalizedURLPath(candidate)
	basePath := strings.TrimRight(normalizedURLPath(base), "/")
	if basePath == "" {
		basePath = "/"
	}
	if basePath == "/" {
		return true
	}
	return candidatePath == basePath || strings.HasPrefix(candidatePath, basePath+"/")
}

func isAuthServerMetadataURLFor(candidate *url.URL, issuerURL *url.URL) bool {
	if candidate == nil || issuerURL == nil {
		return false
	}
	candidatePath := strings.TrimRight(normalizedURLPath(candidate), "/")
	issuerPath := strings.Trim(normalizedURLPath(issuerURL), "/")
	for _, spec := range authServerWellKnownPathSpecs {
		wellKnownPath := strings.TrimRight(spec.Path, "/")
		if candidatePath == wellKnownPath && issuerPath == "" {
			return true
		}
		if issuerPath != "" && candidatePath == wellKnownPath+"/"+issuerPath {
			return true
		}
		if issuerPath != "" && candidatePath == "/"+issuerPath+wellKnownPath {
			return true
		}
	}
	return false
}

func normalizedURLPath(raw *url.URL) string {
	if raw == nil {
		return "/"
	}
	path := raw.EscapedPath()
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func buildURLBundleFromPRMDWithAuthServerMetadata(
	ctx context.Context,
	client *http.Client,
	payload []byte,
	fetchedAt time.Time,
	sourceURL *url.URL,
	options URLBundleOptions,
	logger *slog.Logger,
) (hostbus.URLBundle, *AuthServerMetadataFetchResult, error) {
	var metadata oauthex.ProtectedResourceMetadata
	if err := json.Unmarshal(payload, &metadata); err != nil {
		return hostbus.URLBundle{}, nil, fmt.Errorf("decode protected resource metadata: %w", err)
	}

	records := make([]hostbus.URLRecord, 0, 10)
	bundleGroupID := oauthBundleGroupID(metadata.Resource, metadata.AuthorizationServers, sourceURL)
	trustedOrigin := sourceURL
	if options.TrustedMCPURL != nil {
		trustedOrigin = options.TrustedMCPURL
	}
	resourceRecord := options.apply(urlRecordFromPRMDResource(metadata.Resource, 0))
	resourceRecord = disallowPrivateHostRegistrationOutsideOrigin(resourceRecord, trustedOrigin)
	records = append(records, resourceRecord)

	allowAuthServerPrivateRegistration := false
	if len(metadata.AuthorizationServers) > 0 {
		authServerRecord := options.apply(urlRecordFromPRMDAuthServer(metadata.AuthorizationServers[0], 0, bundleGroupID))
		authServerRecord = disallowPrivateHostRegistrationOutsideOrigin(authServerRecord, trustedOrigin)
		allowAuthServerPrivateRegistration = !authServerRecord.DisallowPrivateHostRegistration
		records = append(records, authServerRecord)
		if len(metadata.AuthorizationServers) > 1 && logger != nil {
			logger.InfoContext(ctx, "oauth PRMD contains multiple authorization servers; only authorization_servers[0] is used",
				slog.Int("authorization_server_count", len(metadata.AuthorizationServers)),
			)
		}
	}
	if sourceURL != nil {
		sourceRecord := options.apply(urlRecordFromPRMDSource(sourceURL, 0))
		sourceRecord = disallowPrivateHostRegistrationOutsideOrigin(sourceRecord, trustedOrigin)
		records = append(records, sourceRecord)
	}

	var authServerMetadataFetch *AuthServerMetadataFetchResult
	if len(metadata.AuthorizationServers) > 0 {
		derivedRecords, fetchResult := buildAuthServerMetadataURLRecords(
			ctx,
			client,
			metadata.AuthorizationServers[0],
			0,
			bundleGroupID,
			allowAuthServerPrivateRegistration,
			options,
			logger,
		)
		authServerMetadataFetch = fetchResult
		records = append(
			records,
			derivedRecords...,
		)
	}

	bundle := hostbus.URLBundle{FetchedAt: fetchedAt}
	bundle.URLs = records
	bundle.URLs = filterURLRecords(bundle.URLs)
	if len(bundle.URLs) == 0 {
		return hostbus.URLBundle{}, authServerMetadataFetch, fmt.Errorf("no urls found in protected resource metadata")
	}
	return bundle, authServerMetadataFetch, nil
}

// BuildURLBundleFromPRMDWithAuthServerMetadata builds a Harpoon registration bundle
// from Protected Resource Metadata payload.
//
// Contract: authorization_servers[0] is the source of truth. Additional
// authorization_servers entries are intentionally ignored for registration and
// auth-server metadata enrichment.
func BuildURLBundleFromPRMDWithAuthServerMetadata(
	ctx context.Context,
	client *http.Client,
	payload []byte,
	fetchedAt time.Time,
	sourceURL *url.URL,
	options URLBundleOptions,
	logger *slog.Logger,
) (hostbus.URLBundle, *AuthServerMetadataFetchResult, error) {
	return buildURLBundleFromPRMDWithAuthServerMetadata(
		ctx,
		client,
		payload,
		fetchedAt,
		sourceURL,
		options,
		logger,
	)
}

func authServerGroup(bundleGroupID string, index int) string {
	return fmt.Sprintf("auth-server:%s:%d", bundleGroupID, index)
}

func urlRecordFromPRMDResource(raw string, index int) hostbus.URLRecord {
	return hostbus.URLRecord{
		URL:         parseURL(raw),
		Description: "PRMD resource",
		Tags:        defaultPRMDTags("prmd-resource", index),
	}
}

func urlRecordFromPRMDAuthServer(raw string, index int, bundleGroupID string) hostbus.URLRecord {
	tags := defaultPRMDTags("prmd-auth-server", index)
	tags = append(tags, hostbus.Tag{Key: hostbus.TagKeyGroup, Value: authServerGroup(bundleGroupID, index)})
	return hostbus.URLRecord{
		URL:         parseURL(raw),
		Description: "PRMD authorization server",
		Tags:        tags,
	}
}

func urlRecordFromPRMDSource(sourceURL *url.URL, index int) hostbus.URLRecord {
	if sourceURL == nil {
		return hostbus.URLRecord{}
	}
	return hostbus.URLRecord{
		URL:         sourceURL,
		Description: "PRMD source URL",
		Tags:        defaultPRMDTags("prmd-source", index),
	}
}

func disallowPrivateHostRegistrationOutsideOrigin(record hostbus.URLRecord, trustedOrigin *url.URL) hostbus.URLRecord {
	if record.URL == nil || trustedOrigin == nil || !sameURLOrigin(record.URL, trustedOrigin) {
		record.DisallowPrivateHostRegistration = true
	}
	return record
}

func defaultPRMDTags(role string, index int) []hostbus.Tag {
	return []hostbus.Tag{
		{Key: hostbus.TagKeySource, Value: "oauth"},
		{Key: hostbus.TagKeyRole, Value: role},
		{Key: hostbus.TagKeyIndex, Value: fmt.Sprintf("%d", index)},
	}
}

func buildAuthServerMetadataURLRecords(
	ctx context.Context,
	client *http.Client,
	authServerRaw string,
	authServerIndex int,
	bundleGroupID string,
	allowPrivateHostRegistration bool,
	options URLBundleOptions,
	logger *slog.Logger,
) ([]hostbus.URLRecord, *AuthServerMetadataFetchResult) {
	issuerURL := parseURL(authServerRaw)
	if issuerURL == nil {
		return nil, &AuthServerMetadataFetchResult{IssuerURL: authServerRaw}
	}
	if client == nil {
		return nil, &AuthServerMetadataFetchResult{IssuerURL: issuerURL.String()}
	}

	meta, fetchResult, err := FetchAuthServerMetadataWithResult(ctx, client, issuerURL.String())
	if fetchResult == nil {
		fetchResult = &AuthServerMetadataFetchResult{IssuerURL: issuerURL.String()}
	}
	if err != nil {
		if logger != nil {
			logger.WarnContext(ctx, "oauth auth-server metadata fetch failed",
				slog.String("issuer", tclog.RedactURL(issuerURL)),
				slog.Int("auth_server_index", authServerIndex),
				slog.String("error", tclog.ErrorForLog(err)),
			)
		}
		return nil, fetchResult
	}
	if logger != nil {
		if mismatchAttempt := selectedIssuerMismatchAttempt(fetchResult); mismatchAttempt != nil {
			logger.WarnContext(ctx, "oauth auth-server metadata issuer differs from authorization_servers[0]",
				slog.String("expected_issuer_url", tclog.RedactURLString(mismatchAttempt.ExpectedIssuerURL)),
				slog.String("metadata_issuer", tclog.RedactURLString(mismatchAttempt.MetadataIssuer)),
				slog.String("selected_metadata_url", tclog.RedactURLString(fetchResult.SelectedURL)),
				slog.Int("auth_server_index", authServerIndex),
			)
		}
	}

	records := make([]hostbus.URLRecord, 0, 7)
	records = appendAuthServerMetadataRecord(
		records,
		fetchResult.SelectedURL,
		"Auth server metadata URL",
		"auth-server-metadata",
		authServerIndex,
		bundleGroupID,
		issuerURL,
		allowPrivateHostRegistration,
		options,
	)
	records = appendAuthServerMetadataRecord(records, meta.Issuer, "Auth server issuer", "issuer", authServerIndex, bundleGroupID, issuerURL, allowPrivateHostRegistration, options)
	records = appendAuthServerMetadataRecord(records, meta.TokenEndpoint, "Auth server token endpoint", "token-endpoint", authServerIndex, bundleGroupID, issuerURL, allowPrivateHostRegistration, options)
	records = appendAuthServerMetadataRecord(records, meta.JWKSURI, "Auth server JWKS URI", "jwks-uri", authServerIndex, bundleGroupID, issuerURL, allowPrivateHostRegistration, options)
	records = appendAuthServerMetadataRecord(records, meta.IntrospectionEndpoint, "Auth server introspection endpoint", "introspection-endpoint", authServerIndex, bundleGroupID, issuerURL, allowPrivateHostRegistration, options)
	records = appendAuthServerMetadataRecord(records, meta.RegistrationEndpoint, "Auth server registration endpoint", "registration-endpoint", authServerIndex, bundleGroupID, issuerURL, allowPrivateHostRegistration, options)
	records = appendAuthServerMetadataRecord(records, meta.RevocationEndpoint, "Auth server revocation endpoint", "revocation-endpoint", authServerIndex, bundleGroupID, issuerURL, allowPrivateHostRegistration, options)
	return records, fetchResult
}

func selectedIssuerMismatchAttempt(fetchResult *AuthServerMetadataFetchResult) *AuthServerMetadataAttempt {
	if fetchResult == nil {
		return nil
	}
	for i := range fetchResult.Attempts {
		attempt := &fetchResult.Attempts[i]
		if attempt.Selected && attempt.IssuerMismatch {
			return attempt
		}
	}
	return nil
}

func appendAuthServerMetadataRecord(
	records []hostbus.URLRecord,
	raw string,
	description string,
	role string,
	authServerIndex int,
	bundleGroupID string,
	issuerURL *url.URL,
	allowPrivateHostRegistration bool,
	options URLBundleOptions,
) []hostbus.URLRecord {
	parsed := parseURL(raw)
	if parsed == nil {
		return records
	}
	return append(records, options.applyAuthServerMetadata(hostbus.URLRecord{
		URL:                             parsed,
		Description:                     description,
		Tags:                            defaultAuthServerMetadataTags(role, authServerIndex, bundleGroupID),
		DisallowPrivateHostRegistration: !allowPrivateHostRegistration || !sameURLOrigin(parsed, issuerURL),
	}, issuerURL))
}

func defaultAuthServerMetadataTags(role string, authServerIndex int, bundleGroupID string) []hostbus.Tag {
	return []hostbus.Tag{
		{Key: hostbus.TagKeySource, Value: "oauth"},
		{Key: hostbus.TagKeyRole, Value: role},
		{Key: hostbus.TagKeyIndex, Value: fmt.Sprintf("%d", authServerIndex)},
		{Key: hostbus.TagKeyGroup, Value: authServerGroup(bundleGroupID, authServerIndex)},
	}
}

func oauthBundleGroupID(resource string, authorizationServers []string, sourceURL *url.URL) string {
	var key string
	switch {
	case sourceURL != nil:
		key = sourceURL.String()
	case resource != "":
		key = resource
	case len(authorizationServers) > 0:
		key = authorizationServers[0]
	default:
		key = "default"
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:6])
}

func parseURL(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil
	}
	return parsed
}

func filterURLRecords(records []hostbus.URLRecord) []hostbus.URLRecord {
	out := make([]hostbus.URLRecord, 0, len(records))
	for _, record := range records {
		if record.URL == nil {
			continue
		}
		out = append(out, record)
	}
	return out
}

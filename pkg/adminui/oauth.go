package adminui

import (
	"encoding/json"
	"time"

	"github.com/openai/tunnel-client/pkg/oauth"
)

type oauthStatusResponse struct {
	DiscoveryURLs        []string                          `json:"discovery_urls,omitempty"`
	Metadata             *oauth.DiscoveryResult            `json:"metadata,omitempty"`
	Error                string                            `json:"error,omitempty"`
	Pending              bool                              `json:"pending,omitempty"`
	WWWAuthenticateProbe *oauth.WWWAuthenticateProbeStatus `json:"www_authenticate_probe,omitempty"`
	MetadataSource       string                            `json:"metadata_source,omitempty"`
	AuthServerMetaMode   string                            `json:"auth_server_metadata_mode"`
	AuthServerCount      int                               `json:"authorization_server_count"`
	SelectedAuthServer   string                            `json:"selected_authorization_server,omitempty"`
}

func buildOAuthStatus(p routeParams) oauthStatusResponse {
	out := oauthStatusResponse{
		AuthServerMetaMode: "authorization_servers[0] only (source of truth)",
	}

	if p.MCPConfig != nil {
		if mainBinding := p.MCPConfig.MainChannelBinding(); mainBinding != nil {
			urls := oauth.BuildResourceMetadataURLs(mainBinding.ServerURL)
			out.DiscoveryURLs = make([]string, 0, len(urls))
			for _, u := range urls {
				if u == nil {
					continue
				}
				out.DiscoveryURLs = append(out.DiscoveryURLs, u.String())
			}
		}
	}

	if p.OAuthState == nil {
		return out
	}

	if result, probe, urls, err, ok := p.OAuthState.Wait(10 * time.Millisecond); ok {
		if len(urls) > 0 {
			out.DiscoveryURLs = urls
		}
		out.WWWAuthenticateProbe = probe
		if err != nil {
			if oauth.IsOptionalDiscoveryFailure(result, probe, err) {
				out.MetadataSource = "not_advertised"
			} else {
				out.Error = err.Error()
			}
		}
		if result != nil {
			out.Metadata = result
			out.SelectedAuthServer, out.AuthServerCount = deriveAuthServerSelection(result)
		}
		if out.MetadataSource == "" {
			out.MetadataSource = deriveMetadataSource(result, probe)
		}
		return out
	}

	out.Pending = true
	return out
}

func deriveMetadataSource(
	result *oauth.DiscoveryResult,
	probe *oauth.WWWAuthenticateProbeStatus,
) string {
	if result == nil || result.URL == "" {
		return ""
	}
	if probe != nil && probe.URL != "" && result.URL == probe.URL {
		return "www_authenticate"
	}
	return "well_known"
}

func deriveAuthServerSelection(result *oauth.DiscoveryResult) (selected string, count int) {
	if result == nil || len(result.Body) == 0 {
		return "", 0
	}
	var payload struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		return "", 0
	}
	count = len(payload.AuthorizationServers)
	if count == 0 {
		return "", 0
	}
	return payload.AuthorizationServers[0], count
}

package cloudflared

import "github.com/openai/tunnel-client/pkg/cloudflared/configgen"

// StandaloneConfig remains as a full-client compatibility alias. The
// standalone renderer itself lives in the full-only configgen package so
// runtime source exports cannot accidentally carry it.
type StandaloneConfig = configgen.StandaloneConfig

// DefaultStandaloneConfig forwards to the full-only config generator.
func DefaultStandaloneConfig(tokenFile string) StandaloneConfig {
	return configgen.DefaultStandaloneConfig(tokenFile)
}

// RenderStandaloneConfig forwards to the full-only config generator.
func RenderStandaloneConfig(cfg StandaloneConfig) ([]byte, error) {
	return configgen.RenderStandaloneConfig(cfg)
}

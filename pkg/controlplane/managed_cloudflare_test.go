package controlplane

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestManagedCloudflareTunnelRuntimeDoesNotSerializeToken(t *testing.T) {
	t.Parallel()

	const token = "managed-runtime-secret-token"
	payload, err := json.Marshal(ManagedCloudflareTunnelRuntime{
		CloudflareTunnel: ManagedCloudflareTunnelMetadata{
			TunnelID:  "provider-tunnel-id",
			Name:      "provider-tunnel-name",
			AccountID: "provider-account-id",
		},
		RuntimeToken: token,
	})
	if err != nil {
		t.Fatalf("marshal managed Cloudflare runtime: %v", err)
	}
	if bytes.Contains(payload, []byte(token)) {
		t.Fatal("serialized managed Cloudflare runtime exposed token")
	}
}

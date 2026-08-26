package mcpserverinfo

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBuildV1ExactProcessAffinePayload(t *testing.T) {
	t.Parallel()

	got, err := BuildV1([]Declaration{
		{Name: "main", ProcessAffinity: true},
		{Name: "harpoon", ProcessAffinity: true},
	})
	if err != nil {
		t.Fatalf("BuildV1 returned error: %v", err)
	}

	const want = `{"version":1,"channels":[{"name":"main","proc_affinity":true},{"name":"harpoon","proc_affinity":true}]}`
	if got != want {
		t.Fatalf("BuildV1() = %s, want %s", got, want)
	}
}

func TestBuildV1OmitsFalseProcessAffinity(t *testing.T) {
	t.Parallel()

	got, err := BuildV1([]Declaration{
		{Name: "main"},
		{Name: "harpoon", ProcessAffinity: true},
	})
	if err != nil {
		t.Fatalf("BuildV1 returned error: %v", err)
	}

	const want = `{"version":1,"channels":[{"name":"main"},{"name":"harpoon","proc_affinity":true}]}`
	if got != want {
		t.Fatalf("BuildV1() = %s, want %s", got, want)
	}
	if strings.Contains(got, `"proc_affinity":false`) {
		t.Fatal("false proc_affinity must be omitted")
	}
}

func TestBuildUsesV1WithoutStatelessCapability(t *testing.T) {
	t.Parallel()

	got, err := Build([]Declaration{
		{Name: "main"},
		{Name: "harpoon", ProcessAffinity: true},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	const want = `{"version":1,"channels":[{"name":"main"},{"name":"harpoon","proc_affinity":true}]}`
	if got != want {
		t.Fatalf("Build() = %s, want %s", got, want)
	}
}

func TestBuildExactV2PayloadAllowsIndependentCapabilities(t *testing.T) {
	t.Parallel()

	got, err := Build([]Declaration{
		{Name: "main", ProcessAffinity: true},
		{Name: "harpoon", Stateless: true, ProcessAffinity: true},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	const want = `{"version":2,"channels":[{"name":"main","proc_affinity":true},{"name":"harpoon","stateless":true,"proc_affinity":true}]}`
	if got != want {
		t.Fatalf("Build() = %s, want %s", got, want)
	}
}

func TestBuildV2OmitsFalseCapabilities(t *testing.T) {
	t.Parallel()

	got, err := Build([]Declaration{
		{Name: "main"},
		{Name: "harpoon", Stateless: true},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	const want = `{"version":2,"channels":[{"name":"main"},{"name":"harpoon","stateless":true}]}`
	if got != want {
		t.Fatalf("Build() = %s, want %s", got, want)
	}
	if strings.Contains(got, `"stateless":false`) {
		t.Fatal("false stateless must be omitted")
	}
	if strings.Contains(got, `"proc_affinity":false`) {
		t.Fatal("false proc_affinity must be omitted")
	}
}

func TestBuildV1RejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		declarations []Declaration
		wantErr      string
	}{
		{
			name:    "empty",
			wantErr: "at least one channel declaration",
		},
		{
			name:         "empty name",
			declarations: []Declaration{{Name: ""}},
			wantErr:      "channel name is required",
		},
		{
			name:         "non canonical name",
			declarations: []Declaration{{Name: " Main "}},
			wantErr:      "is not canonical",
		},
		{
			name:         "invalid name",
			declarations: []Declaration{{Name: "bad/channel"}},
			wantErr:      "invalid channel",
		},
		{
			name: "duplicate",
			declarations: []Declaration{
				{Name: "main"},
				{Name: "main", ProcessAffinity: true},
			},
			wantErr: "duplicate channel declaration",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := BuildV1(testCase.declarations)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("BuildV1 error = %v, want substring %q", err, testCase.wantErr)
			}
		})
	}
}

func TestBuildV2RejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		declarations []Declaration
		wantErr      string
	}{
		{
			name:         "non canonical name",
			declarations: []Declaration{{Name: " Main ", Stateless: true}},
			wantErr:      "is not canonical",
		},
		{
			name:         "invalid name",
			declarations: []Declaration{{Name: "bad/channel", Stateless: true}},
			wantErr:      "invalid channel",
		},
		{
			name: "duplicate",
			declarations: []Declaration{
				{Name: "main", Stateless: true},
				{Name: "main", ProcessAffinity: true},
			},
			wantErr: "duplicate channel declaration",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := Build(testCase.declarations)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Build error = %v, want substring %q", err, testCase.wantErr)
			}
		})
	}
}

func TestBuildV1EnforcesChannelAndByteBounds(t *testing.T) {
	t.Parallel()

	declarations := make([]Declaration, 0, MaxChannels)
	for index := range MaxChannels {
		name := strings.Repeat("a", 62) + fmt.Sprintf("%02d", index)
		if len(name) != 64 {
			t.Fatalf("test channel name length = %d, want 64", len(name))
		}
		declarations = append(declarations, Declaration{
			Name: name,
		})
	}

	got, err := BuildV1(declarations)
	if err != nil {
		t.Fatalf("BuildV1 with %d channels returned error: %v", MaxChannels, err)
	}
	if len(got) > MaxHeaderBytes {
		t.Fatalf("header length = %d, want <= %d", len(got), MaxHeaderBytes)
	}
	if _, err := buildV1(declarations, MaxChannels, len(got)-1); err == nil || !strings.Contains(err.Error(), "serialized header size") {
		t.Fatalf("buildV1 byte overflow error = %v, want serialized header size error", err)
	}

	_, err = BuildV1(append(declarations, Declaration{Name: "overflow"}))
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("BuildV1 overflow error = %v, want channel bound error", err)
	}
}

func TestBuildV2EnforcesChannelAndByteBounds(t *testing.T) {
	t.Parallel()

	declarations := make([]Declaration, 0, MaxChannels)
	for index := range MaxChannels {
		name := strings.Repeat("a", 62) + fmt.Sprintf("%02d", index)
		declarations = append(declarations, Declaration{
			Name:      name,
			Stateless: index == 0,
		})
	}

	got, err := Build(declarations)
	if err != nil {
		t.Fatalf("Build with %d channels returned error: %v", MaxChannels, err)
	}
	if len(got) > MaxHeaderBytes {
		t.Fatalf("header length = %d, want <= %d", len(got), MaxHeaderBytes)
	}
	if _, err := buildV2(declarations, MaxChannels, len(got)-1); err == nil || !strings.Contains(err.Error(), "serialized header size") {
		t.Fatalf("buildV2 byte overflow error = %v, want serialized header size error", err)
	}

	_, err = Build(append(declarations, Declaration{Name: "overflow"}))
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("Build overflow error = %v, want channel bound error", err)
	}
}

func TestBuildV1EmitsOnlyV1Keys(t *testing.T) {
	t.Parallel()

	got, err := BuildV1([]Declaration{{Name: "main", ProcessAffinity: true}})
	if err != nil {
		t.Fatalf("BuildV1 returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("top-level keys = %v, want only version and channels", payload)
	}
	if _, ok := payload["version"]; !ok {
		t.Fatal("header is missing version")
	}
	rawChannels, ok := payload["channels"].([]any)
	if !ok || len(rawChannels) != 1 {
		t.Fatalf("channels = %#v, want one channel", payload["channels"])
	}
	channel, ok := rawChannels[0].(map[string]any)
	if !ok {
		t.Fatalf("channel = %#v, want object", rawChannels[0])
	}
	if len(channel) != 2 {
		t.Fatalf("channel keys = %v, want only name and proc_affinity", channel)
	}
	if _, ok := channel["name"]; !ok {
		t.Fatal("channel is missing name")
	}
	if _, ok := channel["proc_affinity"]; !ok {
		t.Fatal("process-affine channel is missing proc_affinity")
	}
}

func TestBuildV2EmitsOnlyV2Keys(t *testing.T) {
	t.Parallel()

	got, err := Build([]Declaration{{Name: "harpoon", Stateless: true, ProcessAffinity: true}})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("top-level keys = %v, want only version and channels", payload)
	}
	if _, ok := payload["version"]; !ok {
		t.Fatal("header is missing version")
	}
	rawChannels, ok := payload["channels"].([]any)
	if !ok || len(rawChannels) != 1 {
		t.Fatalf("channels = %#v, want one channel", payload["channels"])
	}
	channel, ok := rawChannels[0].(map[string]any)
	if !ok {
		t.Fatalf("channel = %#v, want object", rawChannels[0])
	}
	if len(channel) != 3 {
		t.Fatalf("channel keys = %v, want only name, stateless, and proc_affinity", channel)
	}
	for _, key := range []string{"name", "stateless", "proc_affinity"} {
		if _, ok := channel[key]; !ok {
			t.Fatalf("channel is missing %s", key)
		}
	}
}

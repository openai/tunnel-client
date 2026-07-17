package cloudflared

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json
var manifestJSON []byte

// Manifest is the shipped provenance and platform contract for the bundled
// cloudflared companion.
type Manifest struct {
	Version              string   `json:"version"`
	ReleaseURL           string   `json:"release_url"`
	ReleaseCommit        string   `json:"release_commit"`
	ModulePath           string   `json:"module_path"`
	PackagePath          string   `json:"package_path"`
	ModuleVersion        string   `json:"module_version"`
	ModuleSum            string   `json:"module_sum"`
	GoModSum             string   `json:"go_mod_sum"`
	BuildTime            string   `json:"build_time"`
	SecurityPatchOwner   string   `json:"security_patch_owner"`
	SecurityUpdatePolicy string   `json:"security_update_policy"`
	Platforms            []string `json:"platforms"`
}

var bundledManifest = mustLoadManifest()

// BundledVersion returns the pinned cloudflared version shipped by supported
// tunnel-client distributions.
func BundledVersion() string {
	return bundledManifest.Version
}

// BundledManifest returns the parsed, checked-in provenance manifest.
func BundledManifest() Manifest {
	return bundledManifest
}

func mustLoadManifest() Manifest {
	var manifest Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		panic(fmt.Sprintf("cloudflared: parse bundled manifest: %v", err))
	}
	if manifest.Version == "" ||
		manifest.ReleaseURL == "" ||
		manifest.ReleaseCommit == "" ||
		manifest.ModulePath == "" ||
		manifest.PackagePath == "" ||
		manifest.ModuleVersion == "" ||
		manifest.ModuleSum == "" ||
		manifest.GoModSum == "" ||
		manifest.BuildTime == "" ||
		len(manifest.Platforms) == 0 {
		panic("cloudflared: bundled manifest is missing required fields")
	}
	return manifest
}

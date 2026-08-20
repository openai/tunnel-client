package utils

import (
	"errors"
	"os"
	"os/user"
	"strings"

	"github.com/google/uuid"
	"github.com/openai/tunnel-client/pkg/version"
)

// nsUUID is the namespace used to derive stable IDs.
var nsUUID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://api.openai.com agent:"+version.UserAgent))

// UUIDSeedProvider surfaces a deterministic string segment that feeds the UUID derivation.
type UUIDSeedProvider = func() string

// OsUser returns the current operating system's username or "unknown" if the username cannot be retrieved.
func OsUser() string {
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	return u.Username
}

// OsHost fetches the hostname from the runtime.
func OsHost() string {
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return host
}

// DeriveStableUUID returns a deterministic UUID derived from the provided seed segments.
func DeriveStableUUID(providers ...UUIDSeedProvider) (uuid.UUID, error) {
	if len(providers) == 0 {
		return uuid.Nil, errors.New("no UUID seed providers")
	}

	segments := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			return uuid.Nil, errors.New("nil UUID seed provider")
		}

		value := strings.TrimSpace(provider())
		if value == "" {
			return uuid.Nil, errors.New("empty UUID seed")
		}

		segments = append(segments, value)
	}

	name := strings.Join(segments, ":")
	return uuid.NewSHA1(nsUUID, []byte(name)), nil
}

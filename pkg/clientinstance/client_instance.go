// Package clientinstance owns the opaque identifier for one tunnel-client process.
package clientinstance

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

const randomByteLength = 16

// HeaderName carries the process-scoped ID on control-plane requests.
const HeaderName = "X-Tunnel-Client-Instance-Id"

var currentID = mustNewID()

// ID returns the opaque identifier generated once for this process.
func ID() string {
	return currentID
}

func mustNewID() string {
	id, err := newID(rand.Reader)
	if err != nil {
		panic(err)
	}
	return id
}

func newID(reader io.Reader) (string, error) {
	var raw [randomByteLength]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return "", fmt.Errorf("generate tunnel-client instance ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

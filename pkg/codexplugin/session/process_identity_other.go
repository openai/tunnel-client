//go:build !darwin && !linux && !windows

package session

import "fmt"

// CaptureProcessIdentity fails closed on unsupported operating systems. A PID
// without a stable creation-time and executable fingerprint is unsafe to reuse
// or signal after the launching process exits.
func CaptureProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("process pid must be positive")
	}
	return ProcessIdentity{}, ErrProcessIdentityUnsupported
}

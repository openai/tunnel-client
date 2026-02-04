package types

import (
	"fmt"
	"regexp"
	"strings"
)

type Channel string

const (
	DefaultChannel Channel = "main"
	ChannelHarpoon Channel = "harpoon"
)

var channelPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// NormalizeChannel validates the provided channel and returns DefaultChannel when empty.
func NormalizeChannel(channel string) (Channel, error) {
	canonical := Channel(channel).Canonical()
	if canonical == "" {
		return DefaultChannel, nil
	}
	if channelPattern.MatchString(canonical.String()) {
		return canonical, nil
	}
	return "", fmt.Errorf("invalid channel %q", channel)
}

// String returns the channel name as a string.
func (c Channel) String() string {
	return string(c)
}

// Canonical returns the canonical channel name form used for routing lookups.
// It lowercases and trims surrounding whitespace.
func (c Channel) Canonical() Channel {
	return Channel(strings.ToLower(strings.TrimSpace(c.String())))
}

package peermanagement

import "github.com/LeJamon/go-xrpl/internal/peermanagement/message"

const (
	DefaultMaxManifestPayload    = message.DefaultMaxManifestPayload
	MaxConfiguredManifestPayload = message.MaxConfiguredManifestPayload
)

func MaximumManifestsMessageSize(trustedCount, untrustedCount int) int {
	return message.MaximumManifestsMessageSize(trustedCount, untrustedCount)
}

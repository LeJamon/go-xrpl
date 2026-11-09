package list

import (
	"encoding/hex"
	"slices"

	"github.com/LeJamon/go-xrpl/crypto"
)

// AvailableList returns a detached wire-format snapshot of an available
// publisher list. The boolean is false for malformed, unknown, or unavailable
// publishers; true with a nil map means the output version is unsupported.
func (a *Aggregator) AvailableList(pubKey string, version uint32) (map[string]any, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	rawKey, err := hex.DecodeString(pubKey)
	if err != nil || crypto.PublicKeyType(rawKey) == crypto.KeyTypeUnknown {
		return nil, false
	}

	var key PublisherKey
	copy(key[:], rawKey)
	state, ok := a.state[key]
	if !ok || state == nil || state.Status != StatusAvailable {
		return nil, false
	}
	if !isSupportedVersion(version) {
		return nil, true
	}

	result := map[string]any{
		"manifest":   string(state.RawManifest),
		"version":    version,
		"public_key": pubKey,
	}
	if version == supportedVersionV1 {
		result["blob"] = string(state.RawBlob)
		result["signature"] = string(state.RawSignature)
		if state.RawLocalManifestSet && string(state.RawLocalManifest) != string(state.RawManifest) {
			result["manifest"] = string(state.RawLocalManifest)
		}
		return result, true
	}

	blobs := make([]map[string]any, 0, 1+len(state.Remaining))
	blobs = append(blobs, availableBlob(state.RawBlob, state.RawSignature, state.RawLocalManifest, state.RawLocalManifestSet, state.RawManifest))
	sequences := make([]uint32, 0, len(state.Remaining))
	for sequence := range state.Remaining {
		sequences = append(sequences, sequence)
	}
	slices.Sort(sequences)
	for _, sequence := range sequences {
		pending := state.Remaining[sequence]
		if pending == nil {
			continue
		}
		blobs = append(blobs, availableBlob(
			pending.RawBlob,
			pending.RawSignature,
			pending.RawLocalManifest,
			pending.RawLocalManifestSet,
			state.RawManifest,
		))
	}
	result["blobs_v2"] = blobs
	return result, true
}

func availableBlob(blob, signature, localManifest []byte, localManifestSet bool, outerManifest []byte) map[string]any {
	result := map[string]any{
		"blob":      string(blob),
		"signature": string(signature),
	}
	if localManifestSet && string(localManifest) != string(outerManifest) {
		result["manifest"] = string(localManifest)
	}
	return result
}

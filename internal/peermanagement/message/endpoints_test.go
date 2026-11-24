package message

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func endpointEntryWire() []byte {
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendBytes(entry, []byte("10.0.0.1:51235"))
	entry = protowire.AppendTag(entry, 2, protowire.VarintType)
	return protowire.AppendVarint(entry, 1)
}

func endpointsWire(version uint32, count int) []byte {
	payload := protowire.AppendTag(nil, 1, protowire.VarintType)
	payload = protowire.AppendVarint(payload, uint64(version))
	entry := endpointEntryWire()
	for range count {
		payload = protowire.AppendTag(payload, 3, protowire.BytesType)
		payload = protowire.AppendBytes(payload, entry)
	}
	return payload
}

func TestDecodeEndpointsForPeerBoundsOversizedMaterialization(t *testing.T) {
	decoded, oversized, err := DecodeEndpointsForPeer(endpointsWire(2, maxEndpoints+1))
	if err != nil {
		t.Fatalf("DecodeEndpointsForPeer returned error: %v", err)
	}
	if !oversized {
		t.Fatal("oversized endpoint frame was not flagged")
	}
	if decoded.Version != 2 {
		t.Fatalf("decoded version = %d, want 2", decoded.Version)
	}
	if len(decoded.EndpointsV2) != 0 {
		t.Fatalf("oversized frame materialized %d entries", len(decoded.EndpointsV2))
	}
}

func TestDecodeEndpointsForPeerChecksNestedRequiredFields(t *testing.T) {
	payload := protowire.AppendTag(nil, 1, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 2)
	payload = protowire.AppendTag(payload, 3, protowire.BytesType)
	payload = protowire.AppendBytes(payload, nil)
	if _, _, err := DecodeEndpointsForPeer(payload); err == nil {
		t.Fatal("missing nested required fields were accepted")
	}
}

func TestDecodeEndpointsForPeerUsesLastRepeatedVersion(t *testing.T) {
	payload := protowire.AppendTag(nil, 1, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	payload = append(payload, endpointsWire(2, 1)...)
	decoded, oversized, err := DecodeEndpointsForPeer(payload)
	if err != nil {
		t.Fatalf("DecodeEndpointsForPeer returned error: %v", err)
	}
	if oversized {
		t.Fatal("single endpoint frame was flagged oversized")
	}
	if decoded.Version != 2 {
		t.Fatalf("decoded version = %d, want last value 2", decoded.Version)
	}
}

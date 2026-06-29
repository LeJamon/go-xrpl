package tx

import (
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// TestSignedWithMasterKey pins the shared "signed with the master key" predicate
// that gates AccountSet's asfDisableMaster / asfNoFreeze and the SetRegularKey
// fee waiver. It must match rippled's SetAccount.cpp sigWithMaster: a valid
// public-key type whose derived account ID equals the sender, and false for an
// empty SigningPubKey unless it is an unsigned single-signed transaction in
// skip-verify (standalone) mode.
func TestSignedWithMasterKey(t *testing.T) {
	// EncodeClassicAddressFromPublicKey only hashes the bytes, so any valid
	// 33-byte key (0xED/0x02/0x03 prefix) yields a deterministic address.
	const masterPub = "ED0000000000000000000000000000000000000000000000000000000000000001"
	const otherPub = "ED0000000000000000000000000000000000000000000000000000000000000002"
	// A 33-byte payload whose prefix byte (0x00) is not a valid public-key type:
	// the length check passes but rippled's publicKeyType rejects it, so an
	// arbitrary payload must never derive a master-looking match.
	const badPrefixPub = "00" +
		"0000000000000000000000000000000000000000000000000000000000000000"
	// A 65-byte uncompressed secp256k1 key (0x04 prefix): rippled accepts only
	// 33-byte keys.
	const uncompressedPub = "04" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000"

	masterAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(masterPub)
	if err != nil {
		t.Fatalf("derive master address: %v", err)
	}

	tests := []struct {
		name                string
		skipSigVerification bool
		common              *Common
		want                bool
	}{
		{
			name:   "master-signed",
			common: &Common{Account: masterAddr, SigningPubKey: masterPub},
			want:   true,
		},
		{
			name:   "signed with a non-master key",
			common: &Common{Account: masterAddr, SigningPubKey: otherPub},
			want:   false,
		},
		{
			name:   "malformed SigningPubKey",
			common: &Common{Account: masterAddr, SigningPubKey: "not-hex"},
			want:   false,
		},
		{
			name:   "valid length but invalid public-key type",
			common: &Common{Account: masterAddr, SigningPubKey: badPrefixPub},
			want:   false,
		},
		{
			name:   "65-byte uncompressed key",
			common: &Common{Account: masterAddr, SigningPubKey: uncompressedPub},
			want:   false,
		},
		{
			name:                "empty SigningPubKey, skip-verify, single-signed",
			skipSigVerification: true,
			common:              &Common{Account: masterAddr},
			want:                true,
		},
		{
			name:                "empty SigningPubKey, skip-verify, multi-signed",
			skipSigVerification: true,
			common:              &Common{Account: masterAddr, Signers: []SignerWrapper{{}}},
			want:                false,
		},
		{
			name:   "empty SigningPubKey without skip-verify",
			common: &Common{Account: masterAddr},
			want:   false,
		},
		{
			name:   "nil common",
			common: nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SignedWithMasterKey(tt.skipSigVerification, tt.common); got != tt.want {
				t.Errorf("SignedWithMasterKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

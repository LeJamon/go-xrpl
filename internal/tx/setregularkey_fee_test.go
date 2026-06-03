package tx

import (
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// TestSetRegularKeyFeeWaived pins the single predicate that both the preclaim
// fee floor and the doApply lsfPasswordSpent flag now share. Keeping them in
// lockstep is what prevents the #732 account_hash fork (a full fee charged
// while the flag is still set). Reference: rippled SetRegularKey.cpp
// calculateBaseFee + doApply.
func TestSetRegularKeyFeeWaived(t *testing.T) {
	// EncodeClassicAddressFromPublicKeyHex only hashes the bytes, so any valid
	// 33-byte key (0xED/0x02/0x03 prefix) yields a deterministic address.
	const masterPub = "ED0000000000000000000000000000000000000000000000000000000000000001"
	const otherPub = "ED0000000000000000000000000000000000000000000000000000000000000002"
	// A syntactically valid 65-byte uncompressed secp256k1 key (0x04 prefix):
	// IsValidPublicKey accepts it, but address derivation rejects len != 33, so
	// the combined gate never waives — matching rippled's 33-byte publicKeyType.
	const uncompressedPub = "04" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000"
	masterAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(masterPub)
	if err != nil {
		t.Fatalf("derive master address: %v", err)
	}

	unflagged := &state.AccountRoot{}
	spent := &state.AccountRoot{Flags: state.LsfPasswordSpent}

	tests := []struct {
		name                string
		skipSigVerification bool
		common              *Common
		account             *state.AccountRoot
		want                bool
	}{
		{
			name:    "master-signed and unspent is waived",
			common:  &Common{Account: masterAddr, SigningPubKey: masterPub},
			account: unflagged,
			want:    true,
		},
		{
			name:    "master-signed but already spent is not waived",
			common:  &Common{Account: masterAddr, SigningPubKey: masterPub},
			account: spent,
			want:    false,
		},
		{
			name:    "signed with a non-master key is not waived",
			common:  &Common{Account: masterAddr, SigningPubKey: otherPub},
			account: unflagged,
			want:    false,
		},
		{
			name:    "malformed SigningPubKey is not waived",
			common:  &Common{Account: masterAddr, SigningPubKey: "not-hex"},
			account: unflagged,
			want:    false,
		},
		{
			name:    "invalid public-key type is not waived",
			common:  &Common{Account: masterAddr, SigningPubKey: "00ABCD"},
			account: unflagged,
			want:    false,
		},
		{
			name:    "valid 65-byte uncompressed key is rejected by the address encoder",
			common:  &Common{Account: masterAddr, SigningPubKey: uncompressedPub},
			account: unflagged,
			want:    false,
		},
		{
			name:                "no SigningPubKey with skip-verify is waived",
			skipSigVerification: true,
			common:              &Common{Account: masterAddr},
			account:             unflagged,
			want:                true,
		},
		{
			name:                "no SigningPubKey, skip-verify, multi-signed is not waived",
			skipSigVerification: true,
			common:              &Common{Account: masterAddr, Signers: []SignerWrapper{{}}},
			account:             unflagged,
			want:                false,
		},
		{
			name:    "no SigningPubKey without skip-verify is not waived",
			common:  &Common{Account: masterAddr},
			account: unflagged,
			want:    false,
		},
		{
			name:    "nil account is not waived",
			common:  &Common{Account: masterAddr, SigningPubKey: masterPub},
			account: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SetRegularKeyFeeWaived(tt.skipSigVerification, tt.common, tt.account); got != tt.want {
				t.Errorf("SetRegularKeyFeeWaived() = %v, want %v", got, tt.want)
			}
		})
	}
}

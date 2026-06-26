package tx

import (
	"encoding/hex"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// CheckFlags validates that no unsupported flags are set.
// mask should be the bitwise complement of all valid flags (~(validFlag1 | validFlag2 | ...)).
// If any bit in the mask is set in flags, returns temINVALID_FLAG.
func CheckFlags(flags uint32, mask uint32) error {
	if flags&mask != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "invalid flags")
	}
	return nil
}

// CheckNoFlags validates that zero flags are set.
// Use for transaction types that accept no flags at all.
func CheckNoFlags(flags uint32) error {
	if flags != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "invalid flags")
	}
	return nil
}

// CheckDestNotSrc validates that destination is not the same as source account.
func CheckDestNotSrc(account, destination string) error {
	if account == destination {
		return ter.Errorf(ter.TemDST_IS_SRC, "destination may not be source")
	}
	return nil
}

// CheckDestRequired validates that a destination field is present.
func CheckDestRequired(destination string) error {
	if destination == "" {
		return ter.Errorf(ter.TemDST_NEEDED, "Destination is required")
	}
	return nil
}

// IsValidPublicKey mirrors rippled's publicKeyType() (PublicKey.cpp): a public
// key is valid only if it is exactly 33 bytes prefixed 0xED (ed25519) or
// 0x02 / 0x03 (secp256k1 compressed). rippled never accepts 65-byte
// uncompressed secp256k1 keys, so neither do we.
//
// Address-derivation paths that compare a derived address against an
// account ID must gate on this — otherwise an arbitrary 33-byte
// payload can hex-encode into a valid-looking address.
func IsValidPublicKey(key []byte) bool {
	if len(key) != 33 {
		return false
	}
	return key[0] == 0xED || key[0] == 0x02 || key[0] == 0x03
}

// SignedWithMasterKey reports whether a transaction was signed with the sending
// account's master key: SigningPubKey must hex-decode to a valid public key
// whose derived account ID equals common.Account. A multi-signed transaction
// (empty SigningPubKey, Signers present) is never master-signed; an unsigned
// single-signed transaction is treated as master-signed only when signature
// verification is skipped (standalone/test mode).
//
// This is the single source of truth for the "signed with master" question,
// shared by the SetRegularKey fee waiver and AccountSet's master-key gates so
// the two can never drift. It mirrors rippled's SetAccount.cpp sigWithMaster
// lambda, which gates on publicKeyType() before deriving calcAccountID and
// yields false for an empty SigningPubKey.
func SignedWithMasterKey(skipSigVerification bool, common *Common) bool {
	if common == nil {
		return false
	}
	if spk := common.SigningPubKey; spk != "" {
		spkBytes, decErr := hex.DecodeString(spk)
		if decErr != nil || !IsValidPublicKey(spkBytes) {
			return false
		}
		sigAddr, addrErr := addresscodec.EncodeClassicAddressFromPublicKey(spkBytes)
		return addrErr == nil && sigAddr == common.Account
	}
	return skipSigVerification && len(common.Signers) == 0
}

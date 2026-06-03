package tx

import (
	"encoding/hex"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// SetRegularKeyFeeWaived reports whether a SetRegularKey qualifies for the free
// password-change discount: it is signed with the account's master key while
// lsfPasswordSpent is clear.
//
// This is the single source of truth shared by the preclaim fee floor
// (preclaimBaseFee) and the doApply lsfPasswordSpent flag, so the fee charged
// and the flag can never drift. It mirrors rippled, where both read the one
// computed ctx_.baseFee: the flag is set iff !minimumFee(ctx_.baseFee), and
// ctx_.baseFee is 0 iff this discount applies.
//
// Reference: rippled SetRegularKey.cpp calculateBaseFee (lines 28-49) and
// doApply (lines 83-84).
func SetRegularKeyFeeWaived(skipSigVerification bool, common *Common, account *state.AccountRoot) bool {
	if common == nil || account == nil {
		return false
	}
	// One-shot discount: once lsfPasswordSpent is set the master key must pay
	// the full fee until a fee-paying transaction clears it.
	if account.Flags&state.LsfPasswordSpent != 0 {
		return false
	}
	if spk := common.SigningPubKey; spk != "" {
		// Validate the public-key type before deriving an address, matching
		// rippled's publicKeyType() guard — otherwise an arbitrary payload
		// could hex-encode into a master-looking address.
		spkBytes, decErr := hex.DecodeString(spk)
		if decErr != nil || !IsValidPublicKey(spkBytes) {
			return false
		}
		sigAddr, addrErr := addresscodec.EncodeClassicAddressFromPublicKey(spkBytes)
		return addrErr == nil && sigAddr == common.Account
	}
	// No SigningPubKey: in standalone/test mode signature verification is
	// skipped and a single-signed transaction is treated as master-signed. A
	// multi-signed transaction (Signers present) is never master-signed.
	return skipSigVerification && len(common.Signers) == 0
}

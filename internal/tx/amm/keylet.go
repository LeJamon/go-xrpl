package amm

import (
	"encoding/binary"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// ComputeAMMAccountAddress returns the AMM pseudo-account address for the given asset pair.
// Uses the first 20 bytes of the AMM keylet hash as the account ID.
// Exported for use in test helpers.
func ComputeAMMAccountAddress(asset1, asset2 tx.Asset) string {
	ammKey := computeAMMKeylet(asset1, asset2)
	var accountID [20]byte
	copy(accountID[:], ammKey.Key[:20])
	addr, _ := encodeAccountID(accountID)
	return addr
}

// ComputeAMMKeylet computes the AMM keylet from the asset pair.
// Exported for use in test helpers.
func ComputeAMMKeylet(asset1, asset2 tx.Asset) keylet.Keylet {
	return computeAMMKeylet(asset1, asset2)
}

// PseudoAccountAddress derives the AMM pseudo-account ID for the given keylet key.
// Exported for use in test helpers (e.g., PseudoAccount collision tests).
func PseudoAccountAddress(view tx.LedgerView, parentHash [32]byte, key [32]byte) [20]byte {
	return pseudoAccountAddress(view, parentHash, key)
}

// computeAMMKeylet computes the AMM keylet from the asset pair.
func computeAMMKeylet(asset1, asset2 tx.Asset) keylet.Keylet {
	issuer1 := getIssuerBytes(asset1.Issuer)
	currency1 := state.GetCurrencyBytes(asset1.Currency)
	issuer2 := getIssuerBytes(asset2.Issuer)
	currency2 := state.GetCurrencyBytes(asset2.Currency)

	return keylet.AMM(issuer1, currency1, issuer2, currency2)
}

// getIssuerBytes converts an issuer address string to a 20-byte account ID.
func getIssuerBytes(issuer string) [20]byte {
	if issuer == "" {
		return [20]byte{}
	}
	id, _ := state.DecodeAccountID(issuer)
	return id
}

// maxPseudoAccountAttempts is the number of candidate addresses to try.
// Reference: rippled View.cpp pseudoAccountAddress: maxAccountAttempts = 256
const maxPseudoAccountAttempts = 256

// pseudoAccountAddress derives the AMM pseudo-account ID.
// It tries up to 256 candidate addresses derived from sha512Half(i, parentHash, pseudoOwnerKey),
// then SHA256-RIPEMD160, and returns the first one not already occupied in the ledger.
// Returns the zero AccountID if all 256 slots are taken.
// Reference: rippled View.cpp pseudoAccountAddress (line 1067-1081)
func pseudoAccountAddress(view tx.LedgerView, parentHash [32]byte, pseudoOwnerKey [32]byte) [20]byte {
	for i := range uint16(maxPseudoAccountAttempts) {
		// sha512Half(i, parentHash, pseudoOwnerKey)
		iBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(iBytes, i)
		hash := common.Sha512Half(iBytes, parentHash[:], pseudoOwnerKey[:])

		// ripesha_hasher: SHA256 then RIPEMD160
		accountID := sha256Ripemd160(hash[:])

		// Check if account exists
		acctKey := keylet.Account(accountID)
		if exists, _ := view.Exists(acctKey); !exists {
			return accountID
		}
	}
	return [20]byte{} // All slots taken
}

// sha256Ripemd160 computes SHA256(data) then RIPEMD160 of the result, returning a 20-byte AccountID.
func sha256Ripemd160(data []byte) [20]byte {
	result := addresscodec.Sha256RipeMD160(data)
	var id [20]byte
	copy(id[:], result)
	return id
}

package paychan

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/internal/codec/binary-codec"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	accounttx "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	secp256k1crypto "github.com/LeJamon/goXRPLd/internal/crypto/algorithms/secp256k1"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/stretchr/testify/require"
)

// xrp converts an XRP count to drops.
func xrp(n int64) int64 {
	return n * 1_000_000
}

// drops is an identity function for clarity when specifying amounts in drops.
func drops(n int64) int64 {
	return n
}

// signClaimAuth creates a signature for a payment channel claim authorization.
// It encodes the channel ID and authorized amount into the claim format,
// then signs it with the account's private key using secp256k1.
// Panics on error since this is a test helper.
func signClaimAuth(acc *jtx.Account, channelIDHex string, authAmtDrops uint64) string {
	// Build the claim JSON for encoding
	claimJSON := map[string]any{
		"Channel": channelIDHex,
		"Amount":  strconv.FormatUint(authAmtDrops, 10),
	}

	// Encode for signing claim: produces HashPrefix('CLM\0') + channel_id + amount
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		panic(fmt.Sprintf("signClaimAuth: failed to encode claim: %v", err))
	}

	// Decode the hex message to raw bytes
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		panic(fmt.Sprintf("signClaimAuth: failed to decode message hex: %v", err))
	}

	// The secp256k1 Sign function takes:
	//   msg: raw bytes as a string (NOT hex)
	//   privKey: hex string, 66 chars with "00" prefix
	privKeyHex := "00" + hex.EncodeToString(acc.PrivateKey)
	signature, err := secp256k1crypto.SECP256K1().Sign(string(messageBytes), privKeyHex)
	if err != nil {
		panic(fmt.Sprintf("signClaimAuth: failed to sign: %v", err))
	}

	return signature
}

// chanKeylet returns the keylet for a payment channel given source, destination, and sequence.
func chanKeylet(src, dst *jtx.Account, seq uint32) keylet.Keylet {
	return keylet.PayChannel(src.ID, dst.ID, seq)
}

// chanExists checks if a payment channel exists in the ledger.
func chanExists(env *jtx.TestEnv, k keylet.Keylet) bool {
	return env.LedgerEntryExists(k)
}

// chanBalance returns the balance of a payment channel in drops.
// Panics on error since this is a test helper.
func chanBalance(env *jtx.TestEnv, k keylet.Keylet) uint64 {
	data, err := env.LedgerEntry(k)
	if err != nil {
		panic(fmt.Sprintf("chanBalance: failed to read ledger entry: %v", err))
	}
	channel, err := sle.ParsePayChannel(data)
	if err != nil {
		panic(fmt.Sprintf("chanBalance: failed to parse pay channel: %v", err))
	}
	return channel.Balance
}

// chanAmount returns the total amount deposited in a payment channel in drops.
// Panics on error since this is a test helper.
func chanAmount(env *jtx.TestEnv, k keylet.Keylet) uint64 {
	data, err := env.LedgerEntry(k)
	if err != nil {
		panic(fmt.Sprintf("chanAmount: failed to read ledger entry: %v", err))
	}
	channel, err := sle.ParsePayChannel(data)
	if err != nil {
		panic(fmt.Sprintf("chanAmount: failed to parse pay channel: %v", err))
	}
	return channel.Amount
}

// chanExpiration returns the expiration of a payment channel and whether it is set.
// A zero expiration means the channel has no expiration.
func chanExpiration(env *jtx.TestEnv, k keylet.Keylet) (uint32, bool) {
	data, err := env.LedgerEntry(k)
	if err != nil {
		return 0, false
	}
	channel, err := sle.ParsePayChannel(data)
	if err != nil {
		return 0, false
	}
	return channel.Expiration, channel.Expiration > 0
}

// ownerDirCount returns the number of entries in an account's owner directory.
// This reads the account's OwnerCount field, which tracks the number of owned
// ledger objects (channels, offers, escrows, etc.).
func ownerDirCount(env *jtx.TestEnv, acc *jtx.Account) int {
	// Count entries in the owner directory (matching rippled's ownerDirCount lambda)
	// This counts directory entries, NOT the OwnerCount field on AccountRoot.
	dirKey := keylet.OwnerDir(acc.ID)
	count := 0
	_ = sle.DirForEach(env.Ledger(), dirKey, func(itemKey [32]byte) error {
		count++
		return nil
	})
	return count
}

// inOwnerDir checks if a specific ledger entry (identified by its 32-byte key)
// exists in the account's owner directory. It iterates through all directory pages.
func inOwnerDir(env *jtx.TestEnv, acc *jtx.Account, targetKey [32]byte) bool {
	dirKey := keylet.OwnerDir(acc.ID)
	found := false
	_ = sle.DirForEach(env.Ledger(), dirKey, func(itemKey [32]byte) error {
		if itemKey == targetKey {
			found = true
		}
		return nil
	})
	return found
}

// rmAccount deletes an account from the ledger after advancing enough ledgers.
// It submits an AccountDelete transaction and verifies the result matches expectedCode.
// If expectedCode is "tesSUCCESS", it additionally verifies the account no longer exists.
func rmAccount(t *testing.T, env *jtx.TestEnv, toRm, dst *jtx.Account, expectedCode string) {
	t.Helper()

	// Advance 256 ledgers so the account is eligible for deletion
	env.IncLedgerSeqForAccDel(toRm)

	// Create and submit AccountDelete transaction
	delTx := accounttx.NewAccountDelete(toRm.Address, dst.Address)
	delTx.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
	seq := env.Seq(toRm)
	delTx.Sequence = &seq

	result := env.Submit(delTx)
	require.Equal(t, expectedCode, result.Code,
		"rmAccount: expected result code %s but got %s", expectedCode, result.Code)

	env.Close()

	// If deletion succeeded, verify the account no longer exists
	if expectedCode == "tesSUCCESS" {
		require.False(t, env.Exists(toRm),
			"rmAccount: account %s should not exist after successful deletion", toRm.Name)
	}
}

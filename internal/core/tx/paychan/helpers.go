package paychan

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/internal/codec/address-codec"
	binarycodec "github.com/LeJamon/goXRPLd/internal/codec/binary-codec"
	ed25519crypto "github.com/LeJamon/goXRPLd/internal/crypto/algorithms/ed25519"
	secp256k1crypto "github.com/LeJamon/goXRPLd/internal/crypto/algorithms/secp256k1"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

// serializePayChannel serializes a PayChannel ledger entry from a PaymentChannelCreate transaction.
// This is called during Create and produces the initial SLE bytes.
func serializePayChannel(pcTx *PaymentChannelCreate, ownerID, destID [20]byte, amount uint64) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(destID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "PayChannel",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Amount":          fmt.Sprintf("%d", amount),
		"Balance":         "0",
		"SettleDelay":     pcTx.SettleDelay,
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	}

	if pcTx.CancelAfter != nil {
		jsonObj["CancelAfter"] = *pcTx.CancelAfter
	}

	if pcTx.PublicKey != "" {
		jsonObj["PublicKey"] = pcTx.PublicKey
	}

	if pcTx.SourceTag != nil {
		jsonObj["SourceTag"] = *pcTx.SourceTag
	}

	if pcTx.DestinationTag != nil {
		jsonObj["DestinationTag"] = *pcTx.DestinationTag
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode PayChannel: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// closeChannel closes a payment channel: removes from directories, returns remaining funds
// to owner, decrements OwnerCount, and erases the channel SLE.
// Reference: rippled PayChan.cpp closeChannel() (lines 116-164)
func closeChannel(ctx *tx.ApplyContext, channelKey keylet.Keylet, channel *sle.PayChannelData) tx.Result {
	// 1. Remove from owner directory
	ownerDirKey := keylet.OwnerDir(channel.Account)
	sle.DirRemove(ctx.View, ownerDirKey, channel.OwnerNode, channelKey.Key, false)

	// 2. Remove from destination directory (if fixPayChanRecipientOwnerDir was active when created)
	if channel.HasDestNode {
		destDirKey := keylet.OwnerDir(channel.DestinationID)
		sle.DirRemove(ctx.View, destDirKey, channel.DestinationNode, channelKey.Key, false)
	}

	// 3. Return remaining funds to owner and decrement OwnerCount
	remaining := channel.Amount - channel.Balance

	if channel.Account == ctx.AccountID {
		// Owner is the sender — use ctx.Account (engine writes it back)
		ctx.Account.Balance += remaining
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	} else {
		// Owner is not the sender (dest is closing) — read and update owner directly
		ownerKey := keylet.Account(channel.Account)
		ownerData, err := ctx.View.Read(ownerKey)
		if err != nil || ownerData == nil {
			return tx.TefINTERNAL
		}
		ownerAccount, err := sle.ParseAccountRoot(ownerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		ownerAccount.Balance += remaining
		if ownerAccount.OwnerCount > 0 {
			ownerAccount.OwnerCount--
		}
		ownerUpdated, err := sle.SerializeAccountRoot(ownerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(ownerKey, ownerUpdated); err != nil {
			return tx.TefINTERNAL
		}
	}

	// 4. Erase channel
	if err := ctx.View.Erase(channelKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// verifyClaimSignature verifies a payment channel claim signature.
// The message is: HashPrefix('CLM\0') + channelID (32 bytes) + amount (8 bytes big-endian).
// Reference: rippled serializePayChanAuthorization() in PayChan.h
func verifyClaimSignature(channelIDHex string, amountDrops uint64, pubKeyHex, sigHex string) bool {
	// Build the claim JSON for EncodeForSigningClaim
	claimJSON := map[string]any{
		"Channel": strings.ToUpper(channelIDHex),
		"Amount":  strconv.FormatUint(amountDrops, 10),
	}

	// Encode for signing claim: produces HashPrefix('CLM\0') + channel_id + amount
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		return false
	}

	// Decode the hex message to raw bytes
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return false
	}

	// Verify signature using appropriate algorithm
	msgStr := string(messageBytes)

	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKeyBytes) < 1 {
		return false
	}

	// ED25519 keys start with 0xED prefix
	if pubKeyBytes[0] == 0xED {
		algo := ed25519crypto.ED25519()
		return algo.Validate(msgStr, pubKeyHex, sigHex)
	}

	// Otherwise use secp256k1
	algo := secp256k1crypto.SECP256K1()
	return algo.Validate(msgStr, pubKeyHex, sigHex)
}

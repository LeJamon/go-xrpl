package paychan

import (
	"encoding/hex"
	"math"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// isChannelExpired reports whether a channel time field (CancelAfter or
// Expiration) has passed relative to the parent close time. A zero field is
// treated as absent. fixCleanup3_2_0 makes the comparison strict, fixing an
// off-by-one where an exact match at the close-time instant was treated as
// already expired.
func isChannelExpired(rules *amendment.Rules, closeTime, timeField uint32) bool {
	if timeField == 0 {
		return false
	}
	if rules.FixCleanup3_2_0Enabled() {
		return closeTime > timeField
	}
	return closeTime >= timeField
}

// saturatingAdd adds two uint32 values, clamping at math.MaxUint32 when
// fixCleanup3_2_0 is enabled instead of wrapping around on overflow.
func saturatingAdd(rules *amendment.Rules, lhs, rhs uint32) uint32 {
	if rules.FixCleanup3_2_0Enabled() {
		sum := uint64(lhs) + uint64(rhs)
		if sum > math.MaxUint32 {
			return math.MaxUint32
		}
		return uint32(sum)
	}
	return lhs + rhs
}

// isZeroChannel reports whether a channel ID hex string decodes to the zero
// hash. A zero hash cannot be a ledger key.
func isZeroChannel(channelHex string) bool {
	b, err := hex.DecodeString(channelHex)
	if err != nil || len(b) != 32 {
		return false
	}
	return [32]byte(b) == [32]byte{}
}

// newPayChannelData builds the state.PayChannelData for a PayChannel ledger
// entry from a PaymentChannelCreate transaction. Directory pages
// (OwnerNode/DestinationNode) and the keylet sequence are filled in by the
// caller. The single serializer for a PayChannel entry — creation and
// modification alike — is state.SerializePayChannelFromData.
func newPayChannelData(pcTx *PaymentChannelCreate, ownerID, destID [20]byte, amount uint64) *state.PayChannelData {
	cd := &state.PayChannelData{
		Account:       ownerID,
		DestinationID: destID,
		Amount:        amount,
		Balance:       0,
		SettleDelay:   pcTx.SettleDelay,
		PublicKey:     pcTx.PublicKey,
	}
	if pcTx.CancelAfter != nil {
		cd.CancelAfter = *pcTx.CancelAfter
	}
	if pcTx.SourceTag != nil {
		cd.SourceTag = *pcTx.SourceTag
		cd.HasSourceTag = true
	}
	if pcTx.DestinationTag != nil {
		cd.DestinationTag = *pcTx.DestinationTag
		cd.HasDestTag = true
	}
	return cd
}

// closeChannel closes a payment channel: removes from directories, returns remaining funds
// to owner, decrements OwnerCount, and erases the channel SLE.
// Reference: rippled PayChan.cpp closeChannel() (lines 116-164)
func closeChannel(ctx *tx.ApplyContext, channelKey keylet.Keylet, channel *state.PayChannelData) ter.Result {
	// 1. Remove from owner directory
	ownerDirKey := keylet.OwnerDir(channel.Account)
	if result := tx.DirRemoveOrBadLedger(ctx.View, ownerDirKey, channel.OwnerNode, channelKey.Key); result != ter.TesSUCCESS {
		return result
	}

	// 2. Remove from destination directory (if fixPayChanRecipientOwnerDir was active when created)
	if channel.HasDestNode {
		destDirKey := keylet.OwnerDir(channel.DestinationID)
		if result := tx.DirRemoveOrBadLedger(ctx.View, destDirKey, channel.DestinationNode, channelKey.Key); result != ter.TesSUCCESS {
			return result
		}
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
			return ter.TefINTERNAL
		}
		ownerAccount, err := state.ParseAccountRoot(ownerData)
		if err != nil {
			return ter.TefINTERNAL
		}
		ownerAccount.Balance += remaining
		if ownerAccount.OwnerCount > 0 {
			ownerAccount.OwnerCount--
		}
		ownerUpdated, err := state.SerializeAccountRoot(ownerAccount)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(ownerKey, ownerUpdated); err != nil {
			return ter.TefINTERNAL
		}
	}

	// 4. Erase channel
	if err := ctx.View.Erase(channelKey); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
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
		algo := ed25519.Algorithm{}
		return algo.Validate(msgStr, pubKeyHex, sigHex)
	}

	// Otherwise use secp256k1
	algo := secp256k1.Algorithm{}
	return algo.Validate(msgStr, pubKeyHex, sigHex)
}

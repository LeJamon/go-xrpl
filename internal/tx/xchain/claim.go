package xchain

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func (x *XChainClaim) Preclaim(view tx.ReadOnlyLedgerView, _ tx.EngineConfig) ter.Result {
	bridge, _, err := readBridge(view, x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	destinationID, err := state.DecodeAccountID(x.Destination)
	if err != nil {
		return ter.TecNO_DST
	}
	destination, err := state.ReadAccountRoot(view, destinationID)
	if err != nil {
		return ter.TecINTERNAL
	}
	if destination == nil {
		return ter.TecNO_DST
	}

	dstChain, result := bridgeDestinationSide(bridge, x.XChainBridge)
	if result != ter.TesSUCCESS {
		return result
	}
	if !assetEqual(assetOf(x.Amount), x.XChainBridge.issue(dstChain)) {
		return ter.TecXCHAIN_BAD_TRANSFER_ISSUE
	}
	if bridgeAssetIsNative(x.XChainBridge.LockingChainIssue) != bridgeAssetIsNative(x.XChainBridge.IssuingChainIssue) {
		return ter.TecINTERNAL
	}

	claimBridge, err := claimBridgeKeylet(x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	data, err := view.Read(keylet.XChainClaimID(claimBridge, x.XChainClaimID))
	if err != nil {
		return ter.TecINTERNAL
	}
	if data == nil {
		return ter.TecXCHAIN_NO_CLAIM_ID
	}
	var claim entry.XChainOwnedClaimID
	if err := claim.Decode(data); err != nil {
		return ter.TecINTERNAL
	}
	claimAccount, err := claim.GetAccount()
	if err != nil {
		return ter.TecINTERNAL
	}
	claimAccountAddress, err := state.EncodeAccountID(claimAccount)
	if err != nil {
		return ter.TecINTERNAL
	}
	if claimAccountAddress != x.Account {
		return ter.TecXCHAIN_BAD_CLAIM_ID
	}
	return ter.TesSUCCESS
}

func (x *XChainClaim) Apply(ctx *tx.ApplyContext) ter.Result {
	outer := payment.NewPaymentSandbox(ctx.View)
	outer.SetTransactionContext(ctx.TxHash, ctx.Config.LedgerSequence)
	bridge, _, err := readBridge(outer, x.XChainBridge)
	if err != nil || bridge == nil {
		return ter.TecINTERNAL
	}
	dstChain, result := bridgeDestinationSide(bridge, x.XChainBridge)
	if result != ter.TesSUCCESS {
		return result
	}
	srcChain := otherChain(dstChain)

	claimBridge, err := claimBridgeKeylet(x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	claimKey := keylet.XChainClaimID(claimBridge, x.XChainClaimID)
	data, err := outer.Read(claimKey)
	if err != nil || data == nil {
		return ter.TecINTERNAL
	}
	var claim entry.XChainOwnedClaimID
	if err := claim.Decode(data); err != nil {
		return ter.TecINTERNAL
	}
	attestations, err := claim.GetXChainClaimAttestations()
	if err != nil {
		return ter.TecINTERNAL
	}
	if !attestationsWithinLimit(attestations) {
		return ter.TefEXCEPTION
	}
	signers, result := loadSignerSet(outer, bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	sendingAmount := amountWithAsset(x.Amount, x.XChainBridge.issue(srcChain))
	_, rewards, quorum := claimQuorum(
		outer,
		attestations,
		signers,
		sendingAmount,
		srcChain == lockingChain,
		"",
		false,
	)
	if !quorum {
		return ter.TecXCHAIN_CLAIM_NO_QUORUM
	}
	rewardValue, err := claim.GetSignatureReward()
	if err != nil {
		return ter.TecINTERNAL
	}
	reward, err := state.AmountFromLedgerValue(rewardValue)
	if err != nil {
		return ter.TecINTERNAL
	}
	claimOwner, err := claim.GetAccount()
	if err != nil {
		return ter.TecINTERNAL
	}
	claimOwnerAddress, err := state.EncodeAccountID(claimOwner)
	if err != nil {
		return ter.TecINTERNAL
	}
	final := finalizeClaim(
		ctx, outer, x.XChainBridge, x.Destination, x.DestinationTag, x.Account,
		sendingAmount, claimOwnerAddress, reward, rewards, srcChain, claimKey, keepClaim, true,
	)
	if result := final.result(); result != ter.TesSUCCESS {
		return result
	}
	if err := outer.ApplyToView(ctx.View); err != nil {
		return ter.TecINTERNAL
	}
	syncApplySource(ctx)
	return ter.TesSUCCESS
}

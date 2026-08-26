package xchain

import (
	"errors"
	"strconv"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func bridgeOwnerChain(account string, bridge XChainBridge) chainType {
	if account == bridge.LockingChainDoor {
		return lockingChain
	}
	return issuingChain
}

func (x *XChainCreateBridge) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	for _, chain := range []chainType{issuingChain, lockingChain} {
		bridgeKey, err := bridgeKeylet(x.XChainBridge, chain)
		if err != nil {
			return ter.TecINTERNAL
		}
		exists, err := view.Exists(bridgeKey)
		if err != nil {
			return ter.TecINTERNAL
		}
		if exists {
			return ter.TecDUPLICATE
		}
	}

	chain := bridgeOwnerChain(x.Account, x.XChainBridge)
	issue := x.XChainBridge.issue(chain)
	if !bridgeAssetIsNative(issue) {
		issuerID, err := state.DecodeAccountID(issue.Issuer)
		if err != nil {
			return ter.TecINTERNAL
		}
		issuer, err := state.ReadAccountRoot(view, issuerID)
		if err != nil {
			return ter.TecINTERNAL
		}
		if issuer == nil {
			return ter.TecNO_ISSUER
		}
		if issuer.Flags&state.LsfAllowTrustLineClawback != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	accountID, err := state.DecodeAccountID(x.Account)
	if err != nil {
		return ter.TecINTERNAL
	}
	account, err := state.ReadAccountRoot(view, accountID)
	if err != nil {
		return ter.TecINTERNAL
	}
	if account == nil {
		return ter.TerNO_ACCOUNT
	}
	reserve, ok := tx.AccountReserveForView(view, config, account, tx.ConfineOwnerCount(account.OwnerCount, 1))
	if !ok {
		return ter.TecINTERNAL
	}
	if account.Balance < reserve {
		return ter.TecINSUFFICIENT_RESERVE
	}
	return ter.TesSUCCESS
}

func (x *XChainCreateBridge) Apply(ctx *tx.ApplyContext) ter.Result {
	chain := bridgeOwnerChain(x.Account, x.XChainBridge)
	bridgeKey, err := bridgeKeylet(x.XChainBridge, chain)
	if err != nil {
		return ctx.Internal("XChainCreateBridge.keylet", err)
	}
	dirResult, err := state.DirInsert(ctx.View, keylet.OwnerDir(ctx.AccountID), bridgeKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("XChainCreateBridge.dirInsert", err)
	}

	bridge := &entry.Bridge{}
	bridge.SetAccount(x.Account)
	bridge.SetSignatureReward(mustAmountAny(x.SignatureReward))
	if x.MinAccountCreateAmount != nil {
		bridge.SetMinAccountCreateAmount(mustAmountAny(*x.MinAccountCreateAmount))
	}
	bridge.SetXChainBridge(bridgeMap(x.XChainBridge))
	bridge.SetXChainClaimID("0")
	bridge.SetXChainAccountCreateCount("0")
	bridge.SetXChainAccountClaimCount("0")
	bridge.SetOwnerNode(strconv.FormatUint(dirResult.Page, 16))
	bridge.SetFlags(0)

	ctx.Account.OwnerCount = tx.ConfineOwnerCount(ctx.Account.OwnerCount, 1)
	data, result := encodeEntry(bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	if err := ctx.View.Insert(bridgeKey, data); err != nil {
		return ctx.Internal("XChainCreateBridge.insert", err)
	}
	return ter.TesSUCCESS
}

func (x *XChainModifyBridge) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	chain := bridgeOwnerChain(x.Account, x.XChainBridge)
	bridgeKey, err := bridgeKeylet(x.XChainBridge, chain)
	if err != nil {
		return ter.TecINTERNAL
	}
	data, err := view.Read(bridgeKey)
	if err != nil {
		return ter.TecINTERNAL
	}
	if data == nil {
		return ter.TecNO_ENTRY
	}
	return ter.TesSUCCESS
}

func (x *XChainModifyBridge) Apply(ctx *tx.ApplyContext) ter.Result {
	chain := bridgeOwnerChain(x.Account, x.XChainBridge)
	bridgeKey, err := bridgeKeylet(x.XChainBridge, chain)
	if err != nil {
		return ctx.Internal("XChainModifyBridge.keylet", err)
	}
	data, err := ctx.View.Read(bridgeKey)
	if err != nil || data == nil {
		return ter.TecINTERNAL
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return ctx.Internal("XChainModifyBridge.decode", err)
	}
	if x.SignatureReward != nil {
		fields["SignatureReward"] = mustAmountAny(*x.SignatureReward)
	}
	if x.MinAccountCreateAmount != nil {
		fields["MinAccountCreateAmount"] = mustAmountAny(*x.MinAccountCreateAmount)
	}
	if x.GetFlags()&tfClearAccountCreateAmount != 0 {
		delete(fields, "MinAccountCreateAmount")
	}
	data, err = binarycodec.EncodeBytes(fields)
	if err != nil {
		return ctx.Internal("XChainModifyBridge.encode", err)
	}
	if err := ctx.View.Update(bridgeKey, data); err != nil {
		return ctx.Internal("XChainModifyBridge.update", err)
	}
	return ter.TesSUCCESS
}

func (x *XChainCreateClaimID) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	bridge, _, err := readBridge(view, x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	reward, err := amountFromAny(bridge.SignatureReward)
	if err != nil {
		return ter.TecINTERNAL
	}
	if reward.Compare(x.SignatureReward) != 0 || !assetEqual(assetOf(reward), assetOf(x.SignatureReward)) {
		return ter.TecXCHAIN_REWARD_MISMATCH
	}
	accountID, err := state.DecodeAccountID(x.Account)
	if err != nil {
		return ter.TecINTERNAL
	}
	account, err := state.ReadAccountRoot(view, accountID)
	if err != nil {
		return ter.TecINTERNAL
	}
	if account == nil {
		return ter.TerNO_ACCOUNT
	}
	reserve, ok := tx.AccountReserveForView(view, config, account, tx.ConfineOwnerCount(account.OwnerCount, 1))
	if !ok {
		return ter.TecINTERNAL
	}
	if account.Balance < reserve {
		return ter.TecINSUFFICIENT_RESERVE
	}
	return ter.TesSUCCESS
}

func (x *XChainCreateClaimID) Apply(ctx *tx.ApplyContext) ter.Result {
	bridge, bridgeKey, err := readBridge(ctx.View, x.XChainBridge)
	if err != nil || bridge == nil {
		return ter.TecINTERNAL
	}
	current, err := parseHexUint(bridge.XChainClaimID)
	if err != nil {
		return ter.TecINTERNAL
	}
	claimID := uint64(uint32(current) + 1)
	if claimID == 0 {
		return ter.TecINTERNAL
	}
	claimBridge, err := claimBridgeKeylet(x.XChainBridge)
	if err != nil {
		return ctx.Internal("XChainCreateClaimID.bridgeKeylet", err)
	}
	claimKey := keylet.XChainClaimID(claimBridge, claimID)
	exists, err := ctx.View.Exists(claimKey)
	if err != nil || exists {
		return ter.TecINTERNAL
	}
	dirResult, err := state.DirInsert(ctx.View, keylet.OwnerDir(ctx.AccountID), claimKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("XChainCreateClaimID.dirInsert", err)
	}
	claim := &entry.XChainOwnedClaimID{}
	claim.SetAccount(x.Account)
	claim.SetXChainBridge(bridgeMap(x.XChainBridge))
	claim.SetXChainClaimID(strconv.FormatUint(claimID, 16))
	claim.SetOtherChainSource(x.OtherChainSource)
	claim.SetXChainClaimAttestations([]any{})
	claim.SetSignatureReward(mustAmountAny(x.SignatureReward))
	claim.SetOwnerNode(strconv.FormatUint(dirResult.Page, 16))
	claim.SetFlags(0)
	ctx.Account.OwnerCount = tx.ConfineOwnerCount(ctx.Account.OwnerCount, 1)
	claimData, result := encodeEntry(claim)
	if result != ter.TesSUCCESS {
		return result
	}
	bridge.SetXChainClaimID(strconv.FormatUint(claimID, 16))
	bridgeData, result := encodeEntry(bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	if err := ctx.View.Insert(claimKey, claimData); err != nil {
		return ctx.Internal("XChainCreateClaimID.insert", err)
	}
	if err := ctx.View.Update(bridgeKey, bridgeData); err != nil {
		return ctx.Internal("XChainCreateClaimID.updateBridge", err)
	}
	return ter.TesSUCCESS
}

func (x *XChainCommit) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	bridge, _, err := readBridge(view, x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	if bridge.Account == x.Account {
		return ter.TecXCHAIN_SELF_COMMIT
	}
	chain := lockingChain
	if bridge.Account == x.XChainBridge.IssuingChainDoor {
		chain = issuingChain
	} else if bridge.Account != x.XChainBridge.LockingChainDoor {
		return ter.TecINTERNAL
	}
	if !assetEqual(assetOf(x.Amount), x.XChainBridge.issue(chain)) {
		return ter.TecXCHAIN_BAD_TRANSFER_ISSUE
	}
	return ter.TesSUCCESS
}

func (x *XChainCommit) Apply(ctx *tx.ApplyContext) ter.Result {
	bridge, _, err := readBridge(ctx.View, x.XChainBridge)
	if err != nil || bridge == nil {
		return ter.TecINTERNAL
	}
	return transferFunds(ctx, x.Account, bridge.Account, nil, "", x.Amount, false, false, true)
}

func (x *XChainAccountCreateCommit) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	bridge, _, err := readBridge(view, x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	reward, err := amountFromAny(bridge.SignatureReward)
	if err != nil {
		return ter.TecINTERNAL
	}
	if reward.Compare(x.SignatureReward) != 0 {
		return ter.TecXCHAIN_REWARD_MISMATCH
	}
	if bridge.MinAccountCreateAmount == nil {
		return ter.TecXCHAIN_CREATE_ACCOUNT_DISABLED
	}
	minimum, err := amountFromAny(bridge.MinAccountCreateAmount)
	if err != nil {
		return ter.TecINTERNAL
	}
	if x.Amount.Compare(minimum) < 0 {
		return ter.TecXCHAIN_INSUFF_CREATE_AMOUNT
	}
	if !assetEqual(assetOf(minimum), assetOf(x.Amount)) {
		return ter.TecXCHAIN_BAD_TRANSFER_ISSUE
	}
	if bridge.Account == x.Account {
		return ter.TecXCHAIN_SELF_COMMIT
	}
	srcChain := lockingChain
	if bridge.Account == x.XChainBridge.IssuingChainDoor {
		srcChain = issuingChain
	} else if bridge.Account != x.XChainBridge.LockingChainDoor {
		return ter.TecINTERNAL
	}
	if !assetEqual(assetOf(x.Amount), x.XChainBridge.issue(srcChain)) {
		return ter.TecXCHAIN_BAD_TRANSFER_ISSUE
	}
	if !bridgeAssetIsNative(x.XChainBridge.issue(otherChain(srcChain))) {
		return ter.TecXCHAIN_CREATE_ACCOUNT_NONXRP_ISSUE
	}
	return ter.TesSUCCESS
}

func (x *XChainAccountCreateCommit) Apply(ctx *tx.ApplyContext) ter.Result {
	bridge, bridgeKey, err := readBridge(ctx.View, x.XChainBridge)
	if err != nil || bridge == nil {
		return ter.TecINTERNAL
	}
	total, err := x.Amount.Add(x.SignatureReward)
	if err != nil {
		return ter.TecINTERNAL
	}
	if result := transferFunds(ctx, x.Account, bridge.Account, nil, "", total, true, false, true); result != ter.TesSUCCESS {
		return result
	}
	count, err := parseHexUint(bridge.XChainAccountCreateCount)
	if err != nil {
		return ter.TecINTERNAL
	}
	bridge.SetXChainAccountCreateCount(strconv.FormatUint(count+1, 16))
	data, result := encodeEntry(bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	if err := ctx.View.Update(bridgeKey, data); err != nil {
		return ctx.Internal("XChainAccountCreateCommit.updateBridge", err)
	}
	return ter.TesSUCCESS
}

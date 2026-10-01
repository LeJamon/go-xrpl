package amm

import (
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// ParseAMMData deserializes an AMM ledger entry from binary codec format.
// Exported for use by TrustSet to check LP token balance.
func ParseAMMData(data []byte) (*AMMData, error) {
	return parseAMMData(data)
}

// parseAMMData deserializes an AMM ledger entry from binary codec (SLE) format.
// Reference: rippled include/xrpl/protocol/detail/ledger_entries.macro ltAMM
func parseAMMData(data []byte) (*AMMData, error) {
	var decoded ledgerfields.AMM
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode AMM binary: %w", err)
	}

	account, err := decoded.GetAccount()
	if err != nil {
		return nil, fmt.Errorf("failed to decode AMM Account: %w", err)
	}
	assetValue, err := decoded.GetAsset()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM Asset: %w", err)
	}
	asset, err := issueValueToAsset("Asset", assetValue)
	if err != nil {
		return nil, err
	}
	asset2Value, err := decoded.GetAsset2()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM Asset2: %w", err)
	}
	asset2, err := issueValueToAsset("Asset2", asset2Value)
	if err != nil {
		return nil, err
	}
	lpTokenBalanceValue, err := decoded.GetLPTokenBalance()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM LPTokenBalance: %w", err)
	}
	lpTokenBalance, err := amountValueToAmount("LPTokenBalance", lpTokenBalanceValue)
	if err != nil {
		return nil, err
	}
	ownerNode, err := decoded.GetOwnerNode()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM OwnerNode: %w", err)
	}
	tradingFee, err := decoded.GetTradingFee()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM TradingFee: %w", err)
	}
	voteSlots, err := decoded.GetVoteSlots()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM VoteSlots: %w", err)
	}
	previousTxnLgrSeq, err := decoded.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM PreviousTxnLgrSeq: %w", err)
	}

	amm := &AMMData{
		Account:           account,
		Asset:             asset,
		Asset2:            asset2,
		TradingFee:        tradingFee,
		LPTokenBalance:    lpTokenBalance,
		OwnerNode:         ownerNode,
		VoteSlots:         make([]VoteSlotData, 0, len(voteSlots)),
		PreviousTxnID:     decoded.PreviousTxnID,
		PreviousTxnLgrSeq: previousTxnLgrSeq,
	}

	// VoteSlots (STArray of VoteEntry objects)
	for i, voteEntry := range voteSlots {
		var slot VoteSlotData
		slot.Account, err = voteEntry.GetAccount()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d] Account: %w", i, err)
		}
		slot.TradingFee, err = voteEntry.GetTradingFee()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d] TradingFee: %w", i, err)
		}
		slot.VoteWeight, err = voteEntry.GetVoteWeight()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d] VoteWeight: %w", i, err)
		}
		amm.VoteSlots = append(amm.VoteSlots, slot)
	}

	// AuctionSlot (STObject, optional)
	if decoded.HasAuctionSlot() {
		auctionObj, err := decoded.GetAuctionSlot()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot: %w", err)
		}
		slot := &AuctionSlotData{
			AuthAccounts: make([][20]byte, 0),
		}
		slot.Account, err = auctionObj.GetAccount()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot Account: %w", err)
		}
		slot.Expiration, err = auctionObj.GetExpiration()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot Expiration: %w", err)
		}
		slot.DiscountedFee, err = auctionObj.GetDiscountedFee()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot DiscountedFee: %w", err)
		}
		price, err := auctionObj.GetPrice()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot Price: %w", err)
		}
		slot.Price, err = amountValueToAmount("AuctionSlot Price", price)
		if err != nil {
			return nil, err
		}
		if auctionObj.HasAuthAccounts() {
			authArr, err := auctionObj.GetAuthAccounts()
			if err != nil {
				return nil, fmt.Errorf("failed to parse AMM AuctionSlot AuthAccounts: %w", err)
			}
			slot.AuthAccountsPresent = true
			for i, authAccount := range authArr {
				id, err := authAccount.GetAccount()
				if err != nil {
					return nil, fmt.Errorf("failed to parse AMM AuctionSlot AuthAccounts[%d] Account: %w", i, err)
				}
				slot.AuthAccounts = append(slot.AuthAccounts, id)
			}
		}
		amm.AuctionSlot = slot
	}

	return amm, nil
}

func issueValueToAsset(field string, value ledgerfields.IssueValue) (tx.Asset, error) {
	if value.MPTIssuanceID != "" {
		return tx.Asset{MPTIssuanceID: strings.ToUpper(value.MPTIssuanceID)}, nil
	}
	if value.Currency == "" {
		return tx.Asset{}, fmt.Errorf("failed to parse AMM %s: missing currency", field)
	}
	return tx.Asset{Currency: value.Currency, Issuer: value.Issuer}, nil
}

func amountValueToAmount(field string, value ledgerfields.AmountValue) (tx.Amount, error) {
	var (
		parsed tx.Amount
		err    error
	)
	if value.MPTIssuanceID != "" {
		parsed, err = state.AmountFromLedgerValue(value)
	} else {
		parsed, err = state.NewIssuedAmountFromDecimalString(value.Value, value.Currency, value.Issuer)
	}
	if err != nil {
		return tx.Amount{}, fmt.Errorf("failed to parse AMM %s: %w", field, err)
	}
	return parsed, nil
}

// Reference: rippled include/xrpl/protocol/detail/ledger_entries.macro ltAMM
// IMPORTANT: Asset balances are NOT stored - they are read from AccountRoot/trustlines.
func serializeAMMData(amm *AMMData) ([]byte, error) {
	accountAddr, err := state.EncodeAccountID(amm.Account)
	if err != nil {
		return nil, fmt.Errorf("failed to encode AMM Account: %w", err)
	}

	// Ensure LPTokenBalance has proper currency and issuer.
	// If empty, derive them from the asset pair.
	lptBal := amm.LPTokenBalance
	if lptBal.Currency == "" {
		lptBal = state.NewIssuedAmountFromValue(
			lptBal.Mantissa(), lptBal.Exponent(),
			GenerateAMMLPTCurrencyForAssets(amm.Asset, amm.Asset2),
			accountAddr)
	}

	entry := &ledgerfields.AMM{}
	if err := entry.SetAccountValue(amm.Account); err != nil {
		return nil, fmt.Errorf("failed to encode AMM Account: %w", err)
	}
	if err := entry.SetAssetValue(assetToIssueValue(amm.Asset)); err != nil {
		return nil, fmt.Errorf("failed to encode AMM Asset: %w", err)
	}
	if err := entry.SetAsset2Value(assetToIssueValue(amm.Asset2)); err != nil {
		return nil, fmt.Errorf("failed to encode AMM Asset2: %w", err)
	}
	entry.SetOwnerNodeValue(amm.OwnerNode)
	if err := entry.SetLPTokenBalanceValue(amountToAmountValue(lptBal)); err != nil {
		return nil, fmt.Errorf("failed to encode AMM LPTokenBalance: %w", err)
	}
	entry.SetFlags(0)
	entry.SetTradingFee(amm.TradingFee)

	if amm.PreviousTxnID != "" {
		entry.SetPreviousTxnID(amm.PreviousTxnID)
		entry.SetPreviousTxnLgrSeq(amm.PreviousTxnLgrSeq)
	}

	if len(amm.VoteSlots) > 0 {
		voteSlots := make([]ledgerfields.VoteEntryValue, len(amm.VoteSlots))
		for i, slot := range amm.VoteSlots {
			if err := voteSlots[i].SetAccountValue(slot.Account); err != nil {
				return nil, fmt.Errorf("failed to encode AMM vote account: %w", err)
			}
			voteSlots[i].SetTradingFee(slot.TradingFee)
			voteSlots[i].SetVoteWeight(slot.VoteWeight)
		}
		if err := entry.SetVoteSlotsValue(voteSlots); err != nil {
			return nil, fmt.Errorf("failed to encode AMM VoteSlots: %w", err)
		}
	}

	if amm.AuctionSlot != nil {
		slotPrice := amm.AuctionSlot.Price
		if slotPrice.Currency == "" {
			slotPrice = state.NewIssuedAmountFromValue(
				slotPrice.Mantissa(), slotPrice.Exponent(),
				lptBal.Currency, lptBal.Issuer)
		}
		var auctionSlot ledgerfields.AuctionSlotValue
		if err := auctionSlot.SetAccountValue(amm.AuctionSlot.Account); err != nil {
			return nil, fmt.Errorf("failed to encode AMM auction account: %w", err)
		}
		auctionSlot.SetExpiration(amm.AuctionSlot.Expiration)
		if err := auctionSlot.SetPriceValue(amountToAmountValue(slotPrice)); err != nil {
			return nil, fmt.Errorf("failed to encode AMM auction price: %w", err)
		}
		auctionSlot.SetDiscountedFee(amm.AuctionSlot.DiscountedFee)
		if amm.AuctionSlot.AuthAccountsPresent {
			authAccounts := make([]ledgerfields.AuthAccountValue, len(amm.AuctionSlot.AuthAccounts))
			for i, authID := range amm.AuctionSlot.AuthAccounts {
				if err := authAccounts[i].SetAccountValue(authID); err != nil {
					return nil, fmt.Errorf("failed to encode AMM authorized account: %w", err)
				}
			}
			if err := auctionSlot.SetAuthAccountsValue(authAccounts); err != nil {
				return nil, fmt.Errorf("failed to encode AMM authorized accounts: %w", err)
			}
		}
		if err := entry.SetAuctionSlotValue(auctionSlot); err != nil {
			return nil, fmt.Errorf("failed to encode AMM AuctionSlot: %w", err)
		}
	}

	return entry.Encode()
}

func assetToIssueValue(asset tx.Asset) ledgerfields.IssueValue {
	if asset.IsMPT() {
		return ledgerfields.IssueValue{MPTIssuanceID: strings.ToUpper(asset.MPTIssuanceID)}
	}
	isXRP := isXRPAsset(asset)
	if isXRP {
		return ledgerfields.IssueValue{Currency: "XRP"}
	}
	return ledgerfields.IssueValue{Currency: asset.Currency, Issuer: asset.Issuer}
}

func amountToAmountValue(amt tx.Amount) ledgerfields.AmountValue {
	value := ledgerfields.AmountValue{
		Value:    amt.Value(),
		Currency: amt.Currency,
		Issuer:   amt.Issuer,
	}
	if amt.IsMPT() {
		value.Currency = ""
		value.Issuer = ""
		value.MPTIssuanceID = strings.ToUpper(amt.MPTIssuanceID())
	}
	return value
}

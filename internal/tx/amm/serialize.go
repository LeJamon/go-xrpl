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

	account, err := state.DecodeAccountID(decoded.Account)
	if err != nil {
		return nil, fmt.Errorf("failed to decode AMM Account: %w", err)
	}
	asset, err := issueValueToAsset("Asset", decoded.Asset)
	if err != nil {
		return nil, err
	}
	asset2, err := issueValueToAsset("Asset2", decoded.Asset2)
	if err != nil {
		return nil, err
	}
	lpTokenBalance, err := amountValueToAmount("LPTokenBalance", decoded.LPTokenBalance)
	if err != nil {
		return nil, err
	}
	ownerNode, err := parseHexUint64(decoded.OwnerNode)
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM OwnerNode: %w", err)
	}

	amm := &AMMData{
		Account:           account,
		Asset:             asset,
		Asset2:            asset2,
		TradingFee:        uint16(decoded.TradingFee),
		LPTokenBalance:    lpTokenBalance,
		OwnerNode:         ownerNode,
		VoteSlots:         make([]VoteSlotData, 0, len(decoded.VoteSlots)),
		PreviousTxnID:     decoded.PreviousTxnID,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
	}

	// VoteSlots (STArray of VoteEntry objects)
	for i, entry := range decoded.VoteSlots {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d]: unexpected entry type %T", i, entry)
		}
		voteEntryObj, ok := entryMap["VoteEntry"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d]: missing VoteEntry", i)
		}
		var slot VoteSlotData
		acctStr, ok := voteEntryObj["Account"].(string)
		if !ok {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d]: unexpected Account type %T", i, voteEntryObj["Account"])
		}
		slot.Account, err = state.DecodeAccountID(acctStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM VoteSlots[%d] Account: %w", i, err)
		}
		slot.TradingFee = getFieldUint16(voteEntryObj, "TradingFee")
		slot.VoteWeight = getFieldUint32(voteEntryObj, "VoteWeight")
		amm.VoteSlots = append(amm.VoteSlots, slot)
	}

	// AuctionSlot (STObject, optional)
	if decoded.AuctionSlot != nil {
		auctionObj := decoded.AuctionSlot
		slot := &AuctionSlotData{
			AuthAccounts: make([][20]byte, 0),
		}
		acctStr, ok := auctionObj["Account"].(string)
		if !ok {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot: unexpected Account type %T", auctionObj["Account"])
		}
		slot.Account, err = state.DecodeAccountID(acctStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AMM AuctionSlot Account: %w", err)
		}
		slot.Expiration = getFieldUint32(auctionObj, "Expiration")
		slot.DiscountedFee = getFieldUint16(auctionObj, "DiscountedFee")
		slot.Price, err = amountValueToAmount("AuctionSlot Price", auctionObj["Price"])
		if err != nil {
			return nil, err
		}
		if authArr, ok := auctionObj["AuthAccounts"].([]any); ok {
			slot.AuthAccountsPresent = true
			for i, authEntry := range authArr {
				authMap, ok := authEntry.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("failed to parse AMM AuctionSlot AuthAccounts[%d]: unexpected entry type %T", i, authEntry)
				}
				authAcctObj, ok := authMap["AuthAccount"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("failed to parse AMM AuctionSlot AuthAccounts[%d]: missing AuthAccount", i)
				}
				authAccount, ok := authAcctObj["Account"].(string)
				if !ok {
					return nil, fmt.Errorf("failed to parse AMM AuctionSlot AuthAccounts[%d]: unexpected Account type %T", i, authAcctObj["Account"])
				}
				id, err := state.DecodeAccountID(authAccount)
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

func issueValueToAsset(field string, value any) (tx.Asset, error) {
	issue, ok := value.(map[string]any)
	if !ok {
		return tx.Asset{}, fmt.Errorf("failed to parse AMM %s: unexpected Issue type %T", field, value)
	}
	return issueMapToAsset(issue), nil
}

func amountValueToAmount(field string, value any) (tx.Amount, error) {
	amount, ok := value.(map[string]any)
	if !ok {
		return tx.Amount{}, fmt.Errorf("failed to parse AMM %s: unexpected Amount type %T", field, value)
	}
	parsed, err := amountMapToAmount(amount)
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
	entry.SetAccount(accountAddr)
	entry.SetAsset(assetToIssueMap(amm.Asset))
	entry.SetAsset2(assetToIssueMap(amm.Asset2))
	entry.SetOwnerNode(fmt.Sprintf("%x", amm.OwnerNode))
	entry.SetLPTokenBalance(amountToAmountMap(lptBal))
	entry.SetFlags(0)
	entry.SetTradingFee(amm.TradingFee)

	if amm.PreviousTxnID != "" {
		entry.SetPreviousTxnID(amm.PreviousTxnID)
		entry.SetPreviousTxnLgrSeq(amm.PreviousTxnLgrSeq)
	}

	if len(amm.VoteSlots) > 0 {
		voteSlots := make([]any, 0, len(amm.VoteSlots))
		for _, slot := range amm.VoteSlots {
			slotAcctAddr, err := state.EncodeAccountID(slot.Account)
			if err != nil {
				return nil, fmt.Errorf("failed to encode AMM vote account: %w", err)
			}
			ve := map[string]any{
				"Account":    slotAcctAddr,
				"VoteWeight": slot.VoteWeight,
			}
			if slot.TradingFee != 0 {
				ve["TradingFee"] = slot.TradingFee
			}
			voteEntry := map[string]any{"VoteEntry": ve}
			voteSlots = append(voteSlots, voteEntry)
		}
		entry.SetVoteSlots(voteSlots)
	}

	if amm.AuctionSlot != nil {
		slotAcctAddr, err := state.EncodeAccountID(amm.AuctionSlot.Account)
		if err != nil {
			return nil, fmt.Errorf("failed to encode AMM auction account: %w", err)
		}
		slotPrice := amm.AuctionSlot.Price
		if slotPrice.Currency == "" {
			slotPrice = state.NewIssuedAmountFromValue(
				slotPrice.Mantissa(), slotPrice.Exponent(),
				lptBal.Currency, lptBal.Issuer)
		}
		auctionSlot := map[string]any{
			"Account":    slotAcctAddr,
			"Expiration": amm.AuctionSlot.Expiration,
			"Price":      amountToAmountMap(slotPrice),
		}
		if amm.AuctionSlot.DiscountedFee != 0 {
			auctionSlot["DiscountedFee"] = amm.AuctionSlot.DiscountedFee
		}
		if amm.AuctionSlot.AuthAccountsPresent {
			authAccounts := make([]any, 0, len(amm.AuctionSlot.AuthAccounts))
			for _, authID := range amm.AuctionSlot.AuthAccounts {
				authAcctAddr, err := state.EncodeAccountID(authID)
				if err != nil {
					return nil, fmt.Errorf("failed to encode AMM authorized account: %w", err)
				}
				authAccounts = append(authAccounts, map[string]any{
					"AuthAccount": map[string]any{
						"Account": authAcctAddr,
					},
				})
			}
			auctionSlot["AuthAccounts"] = authAccounts
		}
		entry.SetAuctionSlot(auctionSlot)
	}

	return entry.Encode()
}

// issueMapToAsset converts a binary codec Issue map to a tx.Asset.
func issueMapToAsset(m map[string]any) tx.Asset {
	asset := tx.Asset{}
	if mptID, ok := m["mpt_issuance_id"].(string); ok {
		asset.MPTIssuanceID = strings.ToUpper(mptID)
		return asset
	}
	if currency, ok := m["currency"].(string); ok {
		asset.Currency = currency
	}
	if issuer, ok := m["issuer"].(string); ok {
		asset.Issuer = issuer
	}
	return asset
}

// assetToIssueMap converts a tx.Asset to a binary codec Issue map.
func assetToIssueMap(asset tx.Asset) map[string]any {
	if asset.IsMPT() {
		return map[string]any{"mpt_issuance_id": strings.ToUpper(asset.MPTIssuanceID)}
	}
	isXRP := isXRPAsset(asset)
	if isXRP {
		return map[string]any{"currency": "XRP"}
	}
	return map[string]any{
		"currency": asset.Currency,
		"issuer":   asset.Issuer,
	}
}

// amountMapToAmount converts a binary codec Amount map to a tx.Amount.
func amountMapToAmount(m map[string]any) (tx.Amount, error) {
	valueStr, _ := m["value"].(string)
	currency, _ := m["currency"].(string)
	issuer, _ := m["issuer"].(string)
	return state.NewIssuedAmountFromDecimalString(valueStr, currency, issuer)
}

// amountToAmountMap converts a tx.Amount to a binary codec Amount map.
func amountToAmountMap(amt tx.Amount) map[string]any {
	return map[string]any{
		"value":    amt.Value(),
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
	}
}

// getFieldUint16 extracts a uint16 from a decoded JSON map field.
func getFieldUint16(fields map[string]any, name string) uint16 {
	switch v := fields[name].(type) {
	case float64:
		return uint16(v)
	case int:
		return uint16(v)
	case uint16:
		return v
	}
	return 0
}

// getFieldUint32 extracts a uint32 from a decoded JSON map field.
func getFieldUint32(fields map[string]any, name string) uint32 {
	switch v := fields[name].(type) {
	case float64:
		return uint32(v)
	case int:
		return uint32(v)
	case uint32:
		return v
	}
	return 0
}

// parseHexUint64 parses a hex string to uint64.
func parseHexUint64(s string) (uint64, error) {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" || s == "0" {
		return 0, nil
	}
	var val uint64
	_, err := fmt.Sscanf(s, "%x", &val)
	return val, err
}

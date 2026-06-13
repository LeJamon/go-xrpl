package amm

import (
	"encoding/hex"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// ParseAMMData deserializes an AMM ledger entry from binary codec format.
// Exported for use by TrustSet to check LP token balance.
func ParseAMMData(data []byte) (*AMMData, error) {
	return parseAMMData(data)
}

// parseAMMData deserializes an AMM ledger entry from binary codec (SLE) format.
// The data is first decoded via binarycodec.Decode into a JSON map, then the
// fields are extracted and converted to the AMMData struct.
// Reference: rippled include/xrpl/protocol/detail/ledger_entries.macro ltAMM
func parseAMMData(data []byte) (*AMMData, error) {
	hexStr := hex.EncodeToString(data)
	fields, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode AMM binary: %w", err)
	}

	amm := &AMMData{
		VoteSlots: make([]VoteSlotData, 0),
	}

	// Account (r-address string → [20]byte)
	if acctStr, ok := fields["Account"].(string); ok {
		id, err := state.DecodeAccountID(acctStr)
		if err != nil {
			return nil, fmt.Errorf("failed to decode AMM Account: %w", err)
		}
		amm.Account = id
	}

	// Asset (Issue object)
	if assetObj, ok := fields["Asset"].(map[string]any); ok {
		amm.Asset = issueMapToAsset(assetObj)
	}

	// Asset2 (Issue object)
	if asset2Obj, ok := fields["Asset2"].(map[string]any); ok {
		amm.Asset2 = issueMapToAsset(asset2Obj)
	}

	// TradingFee (UInt16)
	amm.TradingFee = getFieldUint16(fields, "TradingFee")

	// OwnerNode (UInt64 as hex string)
	if ownerNodeStr, ok := fields["OwnerNode"].(string); ok {
		amm.OwnerNode, _ = parseHexUint64(ownerNodeStr)
	}

	// LPTokenBalance (Amount object)
	if lptObj, ok := fields["LPTokenBalance"].(map[string]any); ok {
		amm.LPTokenBalance = amountMapToAmount(lptObj)
	}

	// VoteSlots (STArray of VoteEntry objects)
	if voteSlotsArr, ok := fields["VoteSlots"].([]any); ok {
		for _, entry := range voteSlotsArr {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			voteEntryObj, ok := entryMap["VoteEntry"].(map[string]any)
			if !ok {
				continue
			}
			var slot VoteSlotData
			if acctStr, ok := voteEntryObj["Account"].(string); ok {
				slot.Account, _ = state.DecodeAccountID(acctStr)
			}
			slot.TradingFee = getFieldUint16(voteEntryObj, "TradingFee")
			slot.VoteWeight = getFieldUint32(voteEntryObj, "VoteWeight")
			amm.VoteSlots = append(amm.VoteSlots, slot)
		}
	}

	// AuctionSlot (STObject, optional)
	if auctionObj, ok := fields["AuctionSlot"].(map[string]any); ok {
		slot := &AuctionSlotData{
			AuthAccounts: make([][20]byte, 0),
		}
		if acctStr, ok := auctionObj["Account"].(string); ok {
			slot.Account, _ = state.DecodeAccountID(acctStr)
		}
		slot.Expiration = getFieldUint32(auctionObj, "Expiration")
		slot.DiscountedFee = getFieldUint16(auctionObj, "DiscountedFee")
		if priceObj, ok := auctionObj["Price"].(map[string]any); ok {
			slot.Price = amountMapToAmount(priceObj)
		}
		if authArr, ok := auctionObj["AuthAccounts"].([]any); ok {
			for _, authEntry := range authArr {
				authMap, ok := authEntry.(map[string]any)
				if !ok {
					continue
				}
				authAcctObj, ok := authMap["AuthAccount"].(map[string]any)
				if !ok {
					continue
				}
				if acctStr, ok := authAcctObj["Account"].(string); ok {
					id, err := state.DecodeAccountID(acctStr)
					if err == nil {
						slot.AuthAccounts = append(slot.AuthAccounts, id)
					}
				}
			}
		}
		amm.AuctionSlot = slot
	}

	return amm, nil
}

// serializeAMMData serializes an AMMData entry using the standard binary codec
// format. Builds a JSON map of AMM fields and encodes it via binarycodec.Encode.
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
			GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency),
			accountAddr)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "AMM",
		"Account":         accountAddr,
		"Asset":           assetToIssueMap(amm.Asset),
		"Asset2":          assetToIssueMap(amm.Asset2),
		"OwnerNode":       fmt.Sprintf("%x", amm.OwnerNode),
		"LPTokenBalance":  amountToAmountMap(lptBal),
		// sfFlags is a soeREQUIRED common field (LedgerFormats.cpp commonFields)
		// — present at its default 0 from the SLE template. AMM never sets flags,
		// but rippled still serializes Flags=0; omitting it forks account_hash.
		"Flags": uint32(0),
	}

	// sfTradingFee is soeDEFAULT on ltAMM (ledger_entries.macro:388). rippled's
	// initializeFeeAuctionVote (AMMUtils.cpp:376-379) only sets it when non-zero
	// and makeFieldAbsent's it otherwise, so a 0% AMM has no sfTradingFee in
	// state. Emitting TradingFee:0 forks account_hash.
	if amm.TradingFee != 0 {
		jsonObj["TradingFee"] = amm.TradingFee
	}

	// VoteSlots (STArray of VoteEntry objects)
	if len(amm.VoteSlots) > 0 {
		voteSlots := make([]any, 0, len(amm.VoteSlots))
		for _, slot := range amm.VoteSlots {
			slotAcctAddr, err := state.EncodeAccountID(slot.Account)
			if err != nil {
				continue
			}
			// sfTradingFee is soeDEFAULT on the inner sfVoteEntry template
			// (InnerObjectFormats.cpp:71-74); rippled only sets it when non-zero
			// (AMMUtils.cpp:351-352). Account/VoteWeight are soeREQUIRED.
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
		jsonObj["VoteSlots"] = voteSlots
	}

	// AuctionSlot (STObject, optional)
	if amm.AuctionSlot != nil {
		slotAcctAddr, _ := state.EncodeAccountID(amm.AuctionSlot.Account)
		// Ensure AuctionSlot Price has proper currency and issuer
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
		if len(amm.AuctionSlot.AuthAccounts) > 0 {
			authAccounts := make([]any, 0, len(amm.AuctionSlot.AuthAccounts))
			for _, authID := range amm.AuctionSlot.AuthAccounts {
				authAcctAddr, err := state.EncodeAccountID(authID)
				if err != nil {
					continue
				}
				authAccounts = append(authAccounts, map[string]any{
					"AuthAccount": map[string]any{
						"Account": authAcctAddr,
					},
				})
			}
			auctionSlot["AuthAccounts"] = authAccounts
		}
		jsonObj["AuctionSlot"] = auctionSlot
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode AMM: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// issueMapToAsset converts a binary codec Issue map to a tx.Asset.
func issueMapToAsset(m map[string]any) tx.Asset {
	asset := tx.Asset{}
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
func amountMapToAmount(m map[string]any) tx.Amount {
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

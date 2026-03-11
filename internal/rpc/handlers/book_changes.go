package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// BookChangesMethod handles the book_changes RPC method.
// Computes OHLCV data for all currency pairs that had offer changes in a ledger.
// Reference: rippled BookChanges.h (computeBookChanges)
type BookChangesMethod struct{}

// bookChange tracks OHLCV data for a single currency pair
type bookChange struct {
	CurrencyA string
	CurrencyB string
	VolumeA   *big.Float
	VolumeB   *big.Float
	High      *big.Float
	Low       *big.Float
	Open      *big.Float
	Close     *big.Float
}

func (m *BookChangesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Resolve ledger - default to validated
	ledgerSeq := types.Services.Ledger.GetValidatedLedgerIndex()
	if request.LedgerIndex != "" {
		li := request.LedgerIndex.String()
		switch li {
		case "current":
			ledgerSeq = types.Services.Ledger.GetCurrentLedgerIndex()
		case "closed":
			ledgerSeq = types.Services.Ledger.GetClosedLedgerIndex()
		case "validated":
			ledgerSeq = types.Services.Ledger.GetValidatedLedgerIndex()
		default:
			if n, err := strconv.ParseUint(li, 10, 32); err == nil {
				ledgerSeq = uint32(n)
			}
		}
	}

	targetLedger, err := types.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, types.RpcErrorLgrNotFound("Ledger not found")
	}

	// Collect all offer changes from transaction metadata
	changes := make(map[string]*bookChange)

	targetLedger.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		// Try to parse as StoredTransaction (JSON format from submit handler)
		var storedTx StoredTransaction
		if err := json.Unmarshal(txData, &storedTx); err != nil {
			return true // skip, continue
		}

		if storedTx.Meta == nil {
			return true
		}

		// Get TransactionType to detect OfferCancel
		txType, _ := storedTx.TxJSON["TransactionType"].(string)
		txSequence := uint32(0)
		if seq, ok := storedTx.TxJSON["Sequence"].(float64); ok {
			txSequence = uint32(seq)
		}

		// Get AffectedNodes from metadata
		affectedNodes, ok := storedTx.Meta["AffectedNodes"].([]interface{})
		if !ok {
			return true
		}

		for _, nodeRaw := range affectedNodes {
			node, ok := nodeRaw.(map[string]interface{})
			if !ok {
				continue
			}

			// Only process Modified and Deleted Offer nodes
			var nodeData map[string]interface{}
			var nodeType string

			if mn, ok := node["ModifiedNode"].(map[string]interface{}); ok {
				nodeData = mn
				nodeType = "ModifiedNode"
			} else if dn, ok := node["DeletedNode"].(map[string]interface{}); ok {
				nodeData = dn
				nodeType = "DeletedNode"
			} else {
				continue
			}

			entryType, _ := nodeData["LedgerEntryType"].(string)
			if entryType != "Offer" {
				continue
			}

			finalFields, _ := nodeData["FinalFields"].(map[string]interface{})
			previousFields, _ := nodeData["PreviousFields"].(map[string]interface{})

			if finalFields == nil || previousFields == nil {
				continue
			}

			// Skip explicitly cancelled offers (OfferCancel matching this offer's Sequence)
			if txType == "OfferCancel" && nodeType == "DeletedNode" {
				if offerSeq, ok := finalFields["Sequence"].(float64); ok {
					if uint32(offerSeq) == txSequence {
						continue
					}
				}
			}

			// Compute deltas
			prevGets := parseAmount(previousFields["TakerGets"])
			prevPays := parseAmount(previousFields["TakerPays"])
			finalGets := parseAmount(finalFields["TakerGets"])
			finalPays := parseAmount(finalFields["TakerPays"])

			if prevGets == nil || prevPays == nil || finalGets == nil || finalPays == nil {
				continue
			}

			deltaGets := new(big.Float).Sub(prevGets.value, finalGets.value)
			deltaPays := new(big.Float).Sub(prevPays.value, finalPays.value)

			// Skip if no actual change
			if deltaGets.Sign() == 0 || deltaPays.Sign() == 0 {
				continue
			}

			// Determine currency pair (canonical ordering)
			currA := formatCurrencyKey(finalGets)
			currB := formatCurrencyKey(finalPays)

			// Ensure canonical ordering: XRP first, then alphabetical
			swapped := false
			if currB < currA {
				currA, currB = currB, currA
				deltaGets, deltaPays = deltaPays, deltaGets
				swapped = true
			}
			_ = swapped

			pairKey := currA + "|" + currB

			// Compute exchange rate
			rate := new(big.Float).Quo(
				new(big.Float).Abs(deltaPays),
				new(big.Float).Abs(deltaGets),
			)

			// Update or create change entry
			bc, exists := changes[pairKey]
			if !exists {
				bc = &bookChange{
					CurrencyA: currA,
					CurrencyB: currB,
					VolumeA:   new(big.Float),
					VolumeB:   new(big.Float),
					Open:      new(big.Float).Set(rate),
					High:      new(big.Float).Set(rate),
					Low:       new(big.Float).Set(rate),
					Close:     new(big.Float).Set(rate),
				}
				changes[pairKey] = bc
			} else {
				// Update OHLCV
				if rate.Cmp(bc.High) > 0 {
					bc.High.Set(rate)
				}
				if rate.Cmp(bc.Low) < 0 {
					bc.Low.Set(rate)
				}
				bc.Close.Set(rate)
			}

			// Accumulate volumes (absolute values)
			bc.VolumeA.Add(bc.VolumeA, new(big.Float).Abs(deltaGets))
			bc.VolumeB.Add(bc.VolumeB, new(big.Float).Abs(deltaPays))
		}

		return true
	})

	// Build response
	changesArr := make([]map[string]interface{}, 0, len(changes))
	for _, bc := range changes {
		changesArr = append(changesArr, map[string]interface{}{
			"currency_a": bc.CurrencyA,
			"currency_b": bc.CurrencyB,
			"volume_a":   formatBigFloat(bc.VolumeA),
			"volume_b":   formatBigFloat(bc.VolumeB),
			"high":       formatBigFloat(bc.High),
			"low":        formatBigFloat(bc.Low),
			"open":       formatBigFloat(bc.Open),
			"close":      formatBigFloat(bc.Close),
		})
	}

	ledgerHash := targetLedger.Hash()
	ledgerHashStr := strings.ToUpper(hex.EncodeToString(ledgerHash[:]))

	response := map[string]interface{}{
		"type":         "bookChanges",
		"ledger_index": targetLedger.Sequence(),
		"ledger_hash":  ledgerHashStr,
		"ledger_time":  targetLedger.CloseTime(),
		"validated":    targetLedger.IsValidated(),
		"changes":      changesArr,
	}

	return response, nil
}

// parsedAmount holds a parsed amount with its currency info
type parsedAmount struct {
	value    *big.Float
	currency string
	issuer   string
	isXRP    bool
}

// parseAmount parses an XRPL amount (string for XRP drops, object for IOU)
func parseAmount(raw interface{}) *parsedAmount {
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case string:
		// XRP drops
		drops, ok := new(big.Float).SetString(v)
		if !ok {
			return nil
		}
		return &parsedAmount{value: drops, currency: "XRP", isXRP: true}
	case float64:
		return &parsedAmount{
			value:    new(big.Float).SetFloat64(v),
			currency: "XRP",
			isXRP:    true,
		}
	case map[string]interface{}:
		// IOU amount
		valStr, _ := v["value"].(string)
		if valStr == "" {
			return nil
		}
		val, ok := new(big.Float).SetString(valStr)
		if !ok {
			return nil
		}
		currency, _ := v["currency"].(string)
		issuer, _ := v["issuer"].(string)
		return &parsedAmount{value: val, currency: currency, issuer: issuer}
	}

	return nil
}

// formatCurrencyKey returns the canonical currency string for ordering
func formatCurrencyKey(amt *parsedAmount) string {
	if amt.isXRP {
		return "XRP_drops"
	}
	if amt.issuer != "" {
		return fmt.Sprintf("%s.%s", amt.currency, amt.issuer)
	}
	return amt.currency
}

// formatBigFloat formats a big.Float as a string, removing trailing zeros
func formatBigFloat(f *big.Float) string {
	if f == nil {
		return "0"
	}
	// Check if it's an integer
	if f64, _ := f.Float64(); f64 == math.Trunc(f64) && !math.IsInf(f64, 0) {
		return strconv.FormatInt(int64(f64), 10)
	}
	return f.Text('f', 6)
}

func (m *BookChangesMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *BookChangesMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

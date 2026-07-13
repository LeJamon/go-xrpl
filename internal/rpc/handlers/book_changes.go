package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// BookChangesMethod handles the book_changes RPC method.
// Computes OHLCV data for all currency pairs that had offer changes in a ledger.
// Reference: rippled BookChanges.h (computeBookChanges)
type BookChangesMethod struct{ BaseHandler }

// bookChange tracks OHLCV data for a single currency pair
type bookChange struct {
	CurrencyA      string
	CurrencyB      string
	MPTIssuanceIDA string
	MPTIssuanceIDB string
	VolumeA        state.Amount
	VolumeB        state.Amount
	High           state.Amount
	Low            state.Amount
	Open           state.Amount
	Close          state.Amount
	Domain         string
}

// LedgerWithTransactions is the minimal ledger surface the
// book-changes computation needs: walk every transaction's binary blob
// and answer basic header questions. types.LedgerEntry satisfies it.
// Carved out so ComputeBookChanges can be called from event-source
// wiring code (cli/server.go) without dragging in the full RPC
// service container.
type LedgerWithTransactions interface {
	ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error
	Sequence() uint32
	Hash() [32]byte
	CloseTime() int64
	IsValidated() bool
}

// ComputeBookChanges walks a ledger's transaction metadata and returns
// the per-pair OHLCV bundle rippled emits on the book_changes RPC
// method (and the per-ledger book_changes WebSocket stream).
// The shape of the returned map matches the JSON the RPC handler
// emits, so subscribers can marshal it verbatim into the stream event.
func ComputeBookChanges(l LedgerWithTransactions) map[string]any {
	if l == nil {
		return map[string]any{
			"type":         "bookChanges",
			"ledger_index": uint32(0),
			"changes":      []any{},
		}
	}
	changes := collectBookChanges(l)
	changesArr := make([]map[string]any, 0, len(changes))
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		bc := changes[key]
		change := map[string]any{
			"volume_a": formatBookAmount(bc.VolumeA),
			"volume_b": formatBookAmount(bc.VolumeB),
			"high":     bc.High.IOU().NumberString(),
			"low":      bc.Low.IOU().NumberString(),
			"open":     bc.Open.IOU().NumberString(),
			"close":    bc.Close.IOU().NumberString(),
		}
		if bc.MPTIssuanceIDA != "" {
			change["mpt_issuance_id_a"] = bc.MPTIssuanceIDA
		} else {
			change["currency_a"] = bc.CurrencyA
		}
		if bc.MPTIssuanceIDB != "" {
			change["mpt_issuance_id_b"] = bc.MPTIssuanceIDB
		} else {
			change["currency_b"] = bc.CurrencyB
		}
		if bc.Domain != "" {
			change["domain"] = bc.Domain
		}
		changesArr = append(changesArr, change)
	}
	ledgerHash := l.Hash()
	return map[string]any{
		"type":         "bookChanges",
		"ledger_index": l.Sequence(),
		"ledger_hash":  strings.ToUpper(hex.EncodeToString(ledgerHash[:])),
		"ledger_time":  l.CloseTime(),
		"validated":    l.IsValidated(),
		"changes":      changesArr,
	}
}

func (m *BookChangesMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	var request struct {
		types.LedgerSpecifier
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Resolve the target ledger through the shared lookup: default to current,
	// thread ledger_hash, and reject malformed selectors.
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	request.LedgerSpecifier = parsedLedgerSpec
	targetLedger, _, lerr := LookupLedger(ctx, request.LedgerSpecifier)
	if lerr != nil {
		return nil, lerr
	}

	return ComputeBookChanges(targetLedger), nil
}

// collectBookChanges scans the ledger and returns the per-pair OHLCV
// accumulator map keyed by canonical pair string. Pulled out of the
// RPC handler so both the handler and the book_changes WebSocket
// publisher can call it without duplicating the metadata walk.
func collectBookChanges(targetLedger LedgerWithTransactions) map[string]*bookChange {
	changes := make(map[string]*bookChange)

	_ = targetLedger.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		// Decode VL-encoded binary blob (or JSON fallback)
		storedTx, err := decodeTxBlob(txData)
		if err != nil {
			return true // skip, continue
		}

		if storedTx.Meta == nil {
			return true
		}

		// Get TransactionType to detect OfferCancel/OfferCreate with OfferSequence
		txType, _ := storedTx.TxJSON["TransactionType"].(string)

		// Read OfferSequence from the tx (used by both OfferCancel and OfferCreate
		// to cancel a prior offer). Reference: rippled BookChanges.h lines 67-81
		var offerCancel *uint32
		if txType == "OfferCancel" || txType == "OfferCreate" {
			if v, ok := jsonUint32(storedTx.TxJSON["OfferSequence"]); ok {
				offerCancel = &v
			}
		}

		affectedNodes, ok := storedTx.Meta["AffectedNodes"].([]any)
		if !ok {
			return true
		}

		for _, nodeRaw := range affectedNodes {
			node, ok := nodeRaw.(map[string]any)
			if !ok {
				continue
			}

			// Only process Modified and Deleted Offer nodes
			var nodeData map[string]any
			var nodeType string

			if mn, ok := node["ModifiedNode"].(map[string]any); ok {
				nodeData = mn
				nodeType = "ModifiedNode"
			} else if dn, ok := node["DeletedNode"].(map[string]any); ok {
				nodeData = dn
				nodeType = "DeletedNode"
			} else {
				continue
			}

			entryType, _ := nodeData["LedgerEntryType"].(string)
			if entryType != "Offer" {
				continue
			}

			finalFields, _ := nodeData["FinalFields"].(map[string]any)
			previousFields, _ := nodeData["PreviousFields"].(map[string]any)

			if finalFields == nil || previousFields == nil {
				continue
			}

			// Skip explicitly cancelled offers: filter out deleted offers whose
			// Sequence matches the tx's OfferSequence field.
			// Reference: rippled BookChanges.h lines 112-115
			if nodeType == "DeletedNode" && offerCancel != nil {
				if offerSeq, ok := jsonUint32(finalFields["Sequence"]); ok {
					if offerSeq == *offerCancel {
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

			// Reference: rippled BookChanges.h lines 119-122
			// deltaGets = finalFields.TakerGets - previousFields.TakerGets
			// deltaPays = finalFields.TakerPays - previousFields.TakerPays
			deltaGets, err := finalGets.value.SubUniversal(prevGets.value)
			if err != nil {
				continue
			}
			deltaPays, err := finalPays.value.SubUniversal(prevPays.value)
			if err != nil {
				continue
			}

			// Determine currency pair ordering.
			// Reference: rippled BookChanges.h lines 124-131
			// noswap = isXRP(deltaGets) ? true : (isXRP(deltaPays) ? false : (g < p))
			g := formatCurrencyKey(finalGets)
			p := formatCurrencyKey(finalPays)

			var noswap bool
			if finalGets.isXRP {
				noswap = true
			} else if finalPays.isXRP {
				noswap = false
			} else {
				noswap = g < p
			}

			var first, second state.Amount
			var pairKey string
			if noswap {
				first = deltaGets
				second = deltaPays
				pairKey = g + "|" + p
			} else {
				first = deltaPays
				second = deltaGets
				pairKey = p + "|" + g
			}

			if second.IsZero() {
				continue
			}

			rate := state.DivideNoIssue(first, second)

			if first.IsNegative() {
				first = first.Negate()
			}
			if second.IsNegative() {
				second = second.Negate()
			}

			// Determine currency labels for output
			var currA, currB, mptA, mptB string
			if noswap {
				currA = g
				currB = p
				mptA = finalGets.mptIssuanceID
				mptB = finalPays.mptIssuanceID
			} else {
				currA = p
				currB = g
				mptA = finalPays.mptIssuanceID
				mptB = finalGets.mptIssuanceID
			}
			domain, _ := finalFields["DomainID"].(string)
			domain = strings.ToUpper(domain)

			bc, exists := changes[pairKey]
			if !exists {
				bc = &bookChange{
					CurrencyA:      currA,
					CurrencyB:      currB,
					MPTIssuanceIDA: mptA,
					MPTIssuanceIDB: mptB,
					VolumeA:        first,
					VolumeB:        second,
					Open:           rate,
					High:           rate,
					Low:            rate,
					Close:          rate,
					Domain:         domain,
				}
				changes[pairKey] = bc
			} else {
				volumeA, err := bc.VolumeA.AddUniversal(first)
				if err != nil {
					continue
				}
				volumeB, err := bc.VolumeB.AddUniversal(second)
				if err != nil {
					continue
				}
				bc.VolumeA = volumeA
				bc.VolumeB = volumeB
				if rate.Compare(bc.High) > 0 {
					bc.High = rate
				}
				if rate.Compare(bc.Low) < 0 {
					bc.Low = rate
				}
				bc.Close = rate
				bc.Domain = domain
			}
		}

		return true
	})

	return changes
}

// parsedAmount holds a parsed amount with its currency info
type parsedAmount struct {
	value         state.Amount
	currency      string
	issuer        string
	mptIssuanceID string
	isXRP         bool
}

// parseAmount parses an XRPL amount (string for XRP drops, object for IOU)
func parseAmount(raw any) *parsedAmount {
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case string:
		drops, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil
		}
		return &parsedAmount{value: state.NewXRPAmountFromInt(drops), currency: "XRP", isXRP: true}
	case json.Number:
		drops, err := strconv.ParseInt(v.String(), 10, 64)
		if err != nil {
			return nil
		}
		return &parsedAmount{value: state.NewXRPAmountFromInt(drops), currency: "XRP", isXRP: true}
	case float64:
		const maxExactFloatInteger = 1 << 53
		if math.Trunc(v) != v || v < -maxExactFloatInteger || v > maxExactFloatInteger {
			return nil
		}
		drops, err := strconv.ParseInt(strconv.FormatFloat(v, 'f', -1, 64), 10, 64)
		if err != nil {
			return nil
		}
		return &parsedAmount{
			value:    state.NewXRPAmountFromInt(drops),
			currency: "XRP",
			isXRP:    true,
		}
	case map[string]any:
		valStr, _ := v["value"].(string)
		if valStr == "" {
			return nil
		}
		currency, _ := v["currency"].(string)
		issuer, _ := v["issuer"].(string)
		mptIssuanceID, _ := v["mpt_issuance_id"].(string)
		if mptIssuanceID != "" {
			value, err := strconv.ParseInt(valStr, 10, 64)
			if err != nil {
				return nil
			}
			mptIssuanceID = strings.ToUpper(mptIssuanceID)
			return &parsedAmount{
				value:         state.NewMPTAmountWithIssuanceID(value, "", mptIssuanceID),
				mptIssuanceID: mptIssuanceID,
			}
		}
		value, err := state.NewIssuedAmountFromDecimalString(valStr, currency, issuer)
		if err != nil {
			return nil
		}
		return &parsedAmount{value: value, currency: currency, issuer: issuer}
	}

	return nil
}

// formatCurrencyKey returns the canonical currency string for ordering
func formatCurrencyKey(amt *parsedAmount) string {
	if amt.isXRP {
		return "XRP_drops"
	}
	if amt.mptIssuanceID != "" {
		return amt.mptIssuanceID
	}
	if amt.issuer != "" {
		return fmt.Sprintf("%s/%s", amt.issuer, amt.currency)
	}
	return amt.currency
}

func formatBookAmount(amount state.Amount) string {
	if amount.IsNative() || amount.IsMPT() {
		return amount.Value()
	}
	return amount.IOU().NumberString()
}

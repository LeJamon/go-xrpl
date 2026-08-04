package handlers

import (
	"context"
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

type BookChangesHeader interface {
	Sequence() uint32
	Hash() [32]byte
	CloseTime() int64
	IsValidated() bool
}

type LedgerWithTransactions interface {
	BookChangesHeader
	ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error
}

type BookChangesTransaction struct {
	Transaction map[string]any
	Metadata    map[string]any
}

// ComputeBookChanges walks a ledger's transaction metadata and returns
// the per-pair OHLCV bundle rippled emits on the book_changes RPC
// method (and the per-ledger book_changes WebSocket stream).
// The shape of the returned map matches the JSON the RPC handler
// emits, so subscribers can marshal it verbatim into the stream event.
func ComputeBookChanges(l LedgerWithTransactions) map[string]any {
	result, _ := ComputeBookChangesContext(context.Background(), l)
	return result
}

func ComputeBookChangesContext(ctx context.Context, l LedgerWithTransactions) (map[string]any, error) {
	if l == nil {
		return map[string]any{
			"type":         "bookChanges",
			"ledger_index": uint32(0),
			"changes":      []any{},
		}, nil
	}
	changes, err := collectBookChanges(ctx, l)
	if err != nil {
		return nil, err
	}
	return formatBookChanges(l, changes), nil
}

// ComputeBookChangesFromTransactions computes book changes without decoding raw
// accepted transaction leaves again.
func ComputeBookChangesFromTransactions(l BookChangesHeader, transactions []BookChangesTransaction) map[string]any {
	changes := make(map[string]*bookChange)
	for _, transaction := range transactions {
		accumulateBookChanges(changes, transaction.Transaction, transaction.Metadata)
	}
	return formatBookChanges(l, changes)
}

// ComputeBookChangesFromTransactionsStrict rejects incomplete accepted-ledger
// projections instead of emitting a partial aggregate.
func ComputeBookChangesFromTransactionsStrict(l BookChangesHeader, transactions []BookChangesTransaction) (map[string]any, error) {
	if l == nil {
		return nil, fmt.Errorf("book changes ledger header is nil")
	}
	for i, transaction := range transactions {
		if transaction.Transaction == nil || transaction.Metadata == nil {
			return nil, fmt.Errorf("book changes transaction %d is incomplete", i)
		}
	}
	return ComputeBookChangesFromTransactions(l, transactions), nil
}

func formatBookChanges(l BookChangesHeader, changes map[string]*bookChange) map[string]any {
	if l == nil {
		return map[string]any{
			"type":         "bookChanges",
			"ledger_index": uint32(0),
			"changes":      []any{},
		}
	}
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

func (m *BookChangesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Resolve the target ledger through the shared lookup (rippled
	// RPC::lookupLedger): defaults to current, threads ledger_hash, and rejects
	// a malformed numeric ledger_index with ledgerIndexMalformed instead of
	// silently falling back.
	targetLedger, _, lerr := LookupLedger(ctx, params)
	if lerr != nil {
		return nil, lerr
	}

	result, err := ComputeBookChangesContext(ctx.Context, targetLedger)
	if err != nil {
		return nil, rpcInternalError("book_changes: transaction iteration failed", err)
	}
	return result, nil
}

// collectBookChanges scans the ledger and returns the per-pair OHLCV
// accumulator map keyed by canonical pair string. Pulled out of the
// RPC handler so both the handler and the book_changes WebSocket
// publisher can call it without duplicating the metadata walk.
func collectBookChanges(ctx context.Context, targetLedger LedgerWithTransactions) (map[string]*bookChange, error) {
	changes := make(map[string]*bookChange)
	var decodeErr error

	visit := func(txHash [32]byte, txData []byte) bool {
		storedTx, err := decodeTxBlob(txData)
		if err != nil {
			decodeErr = err
			return false
		}
		accumulateBookChanges(changes, storedTx.TxJSON, storedTx.Meta)
		return true
	}

	var err error
	if contextual, ok := targetLedger.(interface {
		ForEachTransactionContext(context.Context, func([32]byte, []byte) bool) error
	}); ok {
		err = contextual.ForEachTransactionContext(ctx, visit)
	} else {
		err = targetLedger.ForEachTransaction(visit)
	}
	if err != nil {
		return nil, err
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	return changes, nil
}

func accumulateBookChanges(changes map[string]*bookChange, txJSON, metadata map[string]any) {
	if txJSON == nil || metadata == nil {
		return
	}
	txType, _ := txJSON["TransactionType"].(string)
	var offerCancel *uint32
	if txType == "OfferCancel" || txType == "OfferCreate" {
		if value, ok := jsonUint32(txJSON["OfferSequence"]); ok {
			offerCancel = &value
		}
	}
	affectedNodes, ok := metadata["AffectedNodes"].([]any)
	if !ok {
		return
	}
	for _, nodeRaw := range affectedNodes {
		node, ok := nodeRaw.(map[string]any)
		if !ok {
			continue
		}
		var nodeData map[string]any
		var nodeType string
		if modified, ok := node["ModifiedNode"].(map[string]any); ok {
			nodeData = modified
			nodeType = "ModifiedNode"
		} else if deleted, ok := node["DeletedNode"].(map[string]any); ok {
			nodeData = deleted
			nodeType = "DeletedNode"
		} else {
			continue
		}
		if nodeData["LedgerEntryType"] != "Offer" {
			continue
		}
		finalFields, _ := nodeData["FinalFields"].(map[string]any)
		previousFields, _ := nodeData["PreviousFields"].(map[string]any)
		if finalFields == nil || previousFields == nil {
			continue
		}
		if nodeType == "DeletedNode" && offerCancel != nil {
			if offerSeq, ok := jsonUint32(finalFields["Sequence"]); ok && offerSeq == *offerCancel {
				continue
			}
		}
		prevGets := parseAmount(previousFields["TakerGets"])
		prevPays := parseAmount(previousFields["TakerPays"])
		finalGets := parseAmount(finalFields["TakerGets"])
		finalPays := parseAmount(finalFields["TakerPays"])
		if prevGets == nil || prevPays == nil || finalGets == nil || finalPays == nil {
			continue
		}
		deltaGets, err := finalGets.value.SubUniversal(prevGets.value)
		if err != nil {
			continue
		}
		deltaPays, err := finalPays.value.SubUniversal(prevPays.value)
		if err != nil {
			continue
		}
		getsKey := formatCurrencyKey(finalGets)
		paysKey := formatCurrencyKey(finalPays)
		noswap := finalGets.isXRP || !finalPays.isXRP && getsKey < paysKey
		var first, second state.Amount
		var pairKey string
		if noswap {
			first, second = deltaGets, deltaPays
			pairKey = getsKey + "|" + paysKey
		} else {
			first, second = deltaPays, deltaGets
			pairKey = paysKey + "|" + getsKey
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
		var currencyA, currencyB, mptA, mptB string
		if noswap {
			currencyA, currencyB = getsKey, paysKey
			mptA, mptB = finalGets.mptIssuanceID, finalPays.mptIssuanceID
		} else {
			currencyA, currencyB = paysKey, getsKey
			mptA, mptB = finalPays.mptIssuanceID, finalGets.mptIssuanceID
		}
		domain, _ := finalFields["DomainID"].(string)
		domain = strings.ToUpper(domain)
		change := changes[pairKey]
		if change == nil {
			changes[pairKey] = &bookChange{
				CurrencyA: currencyA, CurrencyB: currencyB,
				MPTIssuanceIDA: mptA, MPTIssuanceIDB: mptB,
				VolumeA: first, VolumeB: second,
				Open: rate, High: rate, Low: rate, Close: rate, Domain: domain,
			}
			continue
		}
		volumeA, err := change.VolumeA.AddUniversal(first)
		if err != nil {
			continue
		}
		volumeB, err := change.VolumeB.AddUniversal(second)
		if err != nil {
			continue
		}
		change.VolumeA, change.VolumeB = volumeA, volumeB
		if rate.Compare(change.High) > 0 {
			change.High = rate
		}
		if rate.Compare(change.Low) < 0 {
			change.Low = rate
		}
		change.Close = rate
		change.Domain = domain
	}
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

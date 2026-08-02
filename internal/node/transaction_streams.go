package node

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

func extractBookPairsFromMetadata(metaJSON []byte) []types.OrderBookSpec {
	var metadata map[string]any
	if err := json.Unmarshal(metaJSON, &metadata); err != nil {
		return nil
	}
	return extractBookPairsFromMetadataMap(metadata)
}

func extractBookPairsFromMetadataMap(metadata map[string]any) []types.OrderBookSpec {
	seen := make(map[string]struct{})
	var out []types.OrderBookSpec
	nodes, _ := metadata["AffectedNodes"].([]any)
	for _, rawNode := range nodes {
		node, _ := rawNode.(map[string]any)
		var raw map[string]any
		var fieldName string
		switch {
		case node["ModifiedNode"] != nil:
			raw, _ = node["ModifiedNode"].(map[string]any)
			fieldName = "PreviousFields"
		case node["CreatedNode"] != nil:
			raw, _ = node["CreatedNode"].(map[string]any)
			fieldName = "NewFields"
		case node["DeletedNode"] != nil:
			raw, _ = node["DeletedNode"].(map[string]any)
			fieldName = "FinalFields"
		default:
			continue
		}
		if raw == nil || raw["LedgerEntryType"] != "Offer" {
			continue
		}
		fields, _ := raw[fieldName].(map[string]any)
		if fields == nil {
			continue
		}
		gets := currencySpecFromAmount(fields["TakerGets"])
		pays := currencySpecFromAmount(fields["TakerPays"])
		if !validBookCurrencySpec(gets) || !validBookCurrencySpec(pays) {
			continue
		}
		domain, _ := fields["DomainID"].(string)
		book := types.OrderBookSpec{TakerGets: gets, TakerPays: pays, Domain: domain}
		key := gets.Currency + "\x00" + gets.Issuer + "\x00" + strings.ToUpper(gets.MPTIssuanceID) + "\x00" +
			pays.Currency + "\x00" + pays.Issuer + "\x00" + strings.ToUpper(pays.MPTIssuanceID) + "\x00" + strings.ToUpper(domain)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, book)
	}
	return out
}

func currencySpecFromAmount(raw any) types.CurrencySpec {
	switch v := raw.(type) {
	case string:
		return types.CurrencySpec{Currency: "XRP"}
	case map[string]any:
		currency, _ := v["currency"].(string)
		issuer, _ := v["issuer"].(string)
		mptIssuanceID, _ := v["mpt_issuance_id"].(string)
		return types.CurrencySpec{Currency: currency, Issuer: issuer, MPTIssuanceID: mptIssuanceID}
	default:
		return types.CurrencySpec{}
	}
}

func validBookCurrencySpec(spec types.CurrencySpec) bool {
	return spec.Currency != "" || spec.MPTIssuanceID != ""
}

func buildProposedTxEvent(ev service.SubmittedTxEvent) *rpc.ProposedTransactionEvent {
	txJSON := json.RawMessage("{}")
	if len(ev.RawBlob) > 0 {
		if decoded, err := binarycodec.Decode(hex.EncodeToString(ev.RawBlob)); err == nil {
			if ev.OwnerFunds != "" {
				decoded["owner_funds"] = ev.OwnerFunds
			}
			if encoded, err := json.Marshal(decoded); err == nil {
				txJSON = encoded
			}
		}
	}
	return rpc.NewProposedTransactionEvent(
		txJSON,
		ev.Result.Name,
		ev.Result.Code,
		ev.Result.Message,
		ev.CurrentLedger,
		upperHex(ev.TxHash[:]),
	)
}

type acceptedTransactionProjection struct {
	transaction      map[string]any
	metadata         map[string]any
	result           ter.Result
	transactionIndex uint32
	affectedAccounts []string
}

type acceptedPublication struct {
	projection   *acceptedTransactionProjection
	event        *rpc.TransactionEvent
	engineResult ter.Result
}

type acceptedProjectionFailure struct {
	hash [32]byte
	err  error
}

func projectAcceptedTransaction(
	accepted *service.AcceptedTransaction,
	ctx handlers.SyntheticMetadataContext,
) (*acceptedTransactionProjection, error) {
	snapshot, err := accepted.Projection()
	if err != nil {
		return nil, err
	}
	transaction := snapshot.Transaction
	metadata := snapshot.Metadata
	if transaction == nil || metadata == nil {
		return nil, errors.New("accepted transaction projection is incomplete")
	}
	handlers.InjectSyntheticFields(transaction, metadata, ctx)
	return &acceptedTransactionProjection{
		transaction:      transaction,
		metadata:         metadata,
		result:           snapshot.Result,
		transactionIndex: snapshot.TransactionIndex,
		affectedAccounts: snapshot.AffectedAccounts,
	}, nil
}

func projectTransactionResult(
	txResult service.TransactionResultEvent,
	ctx handlers.SyntheticMetadataContext,
) (*acceptedTransactionProjection, error) {
	accepted := txResult.Accepted
	if accepted == nil {
		accepted = service.ParseAcceptedTransaction(txResult.TxData)
	}
	return projectAcceptedTransaction(accepted, ctx)
}

func prepareAcceptedPublications(
	event *service.LedgerAcceptedEvent,
	defaultNetworkID uint32,
) ([]acceptedPublication, []handlers.BookChangesTransaction, []acceptedProjectionFailure) {
	if event == nil || event.LedgerInfo == nil {
		return nil, nil, nil
	}
	publications := make([]acceptedPublication, 0, len(event.TransactionResults))
	bookTransactions := make([]handlers.BookChangesTransaction, 0, len(event.TransactionResults))
	var failures []acceptedProjectionFailure
	for _, txResult := range event.TransactionResults {
		projection, err := projectTransactionResult(txResult, handlers.SyntheticMetadataContext{
			LedgerSequence: txResult.LedgerIndex,
			CloseTime:      rippleEpochSeconds(event.LedgerInfo.CloseTime),
		})
		if err != nil {
			failures = append(failures, acceptedProjectionFailure{hash: txResult.TxHash, err: err})
			continue
		}
		txEvent, engineResult, err := buildProjectedValidatedTransactionEvent(txResult, projection, event, defaultNetworkID)
		if err != nil {
			failures = append(failures, acceptedProjectionFailure{hash: txResult.TxHash, err: err})
			continue
		}
		bookTransactions = append(bookTransactions, handlers.BookChangesTransaction{
			Transaction: projection.transaction,
			Metadata:    projection.metadata,
		})
		publications = append(publications, acceptedPublication{
			projection:   projection,
			event:        txEvent,
			engineResult: engineResult,
		})
	}
	return publications, bookTransactions, failures
}

func acceptedOrderBookPairs(publication acceptedPublication) []types.OrderBookSpec {
	if publication.engineResult != ter.TesSUCCESS || publication.projection == nil {
		return nil
	}
	return extractBookPairsFromMetadataMap(publication.projection.metadata)
}

func buildValidatedTransactionEvent(
	txResult service.TransactionResultEvent,
	event *service.LedgerAcceptedEvent,
	defaultNetworkID uint32,
) (*rpc.TransactionEvent, ter.Result, error) {
	projection, err := projectTransactionResult(txResult, handlers.SyntheticMetadataContext{
		LedgerSequence: txResult.LedgerIndex,
		CloseTime:      rippleEpochSeconds(event.LedgerInfo.CloseTime),
	})
	if err != nil {
		return nil, ter.TemMALFORMED, err
	}
	return buildProjectedValidatedTransactionEvent(txResult, projection, event, defaultNetworkID)
}

func buildProjectedValidatedTransactionEvent(
	txResult service.TransactionResultEvent,
	projection *acceptedTransactionProjection,
	event *service.LedgerAcceptedEvent,
	defaultNetworkID uint32,
) (*rpc.TransactionEvent, ter.Result, error) {
	info := event.LedgerInfo
	closeTime := rippleEpochSeconds(info.CloseTime)
	engineResult := projection.result
	metaJSON, err := json.Marshal(projection.metadata)
	if err != nil {
		return nil, ter.TemMALFORMED, fmt.Errorf("marshal accepted metadata: %w", err)
	}
	txFields := maps.Clone(projection.transaction)

	streamEvent := &rpc.TransactionEvent{
		Type:                "transaction",
		EngineResult:        engineResult.String(),
		EngineResultCode:    int(engineResult),
		EngineResultMessage: engineResult.Message(),
		LedgerIndex:         txResult.LedgerIndex,
		LedgerHash:          upperHex(info.Hash[:]),
		Meta:                metaJSON,
		Hash:                upperHex(txResult.TxHash[:]),
		Validated:           txResult.Validated,
		Status:              "closed",
	}
	if !txResult.Validated {
		txJSON, err := json.Marshal(txFields)
		if err != nil {
			return nil, ter.TemMALFORMED, fmt.Errorf("marshal accepted transaction: %w", err)
		}
		streamEvent.Transaction = txJSON
		return streamEvent, engineResult, nil
	}

	if !info.CloseTime.IsZero() {
		streamEvent.CloseTimeISO = info.CloseTime.UTC().Format(time.RFC3339)
	}

	if closeTime >= 0 && closeTime <= int64(^uint32(0)) {
		txFields["date"] = uint32(closeTime)
	}
	if event.Ledger != nil {
		_, reserveBase, reserveInc := service.FeesFromLedger(event.Ledger)
		if funds, ok := handlers.TransactionOwnerFunds(txFields, event.Ledger, reserveBase, reserveInc); ok {
			txFields["owner_funds"] = funds
		}
	}
	encoded, err := json.Marshal(txFields)
	if err != nil {
		return nil, ter.TemMALFORMED, fmt.Errorf("marshal projected transaction: %w", err)
	}
	streamEvent.Transaction = encoded

	networkID := defaultNetworkID
	if raw, exists := txFields["NetworkID"]; exists {
		if override, valid := uint32JSONValue(raw); valid {
			networkID = override
		}
	}
	if ctid, ok := handlers.EncodeCTID(txResult.LedgerIndex, projection.transactionIndex, networkID); ok {
		streamEvent.CTID = ctid
	}
	return streamEvent, engineResult, nil
}

func rippleEpochSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	seconds := t.Unix() - protocol.RippleEpochUnix
	if seconds < 0 {
		return 0
	}
	return seconds
}

func uint32JSONValue(value any) (uint32, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	var parsed uint32
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}

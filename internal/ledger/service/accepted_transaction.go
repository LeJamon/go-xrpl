package service

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const (
	minSerializedTransactionBytes = 32
	maxSerializedTransactionBytes = 1 << 20
)

// AcceptedTransaction is the immutable decoded form of a transaction leaf in
// an accepted ledger. Its accessors return owned copies of mutable values.
type AcceptedTransaction struct {
	raw              []byte
	transactionBlob  []byte
	metadataBlob     []byte
	transaction      map[string]any
	metadata         map[string]any
	result           ter.Result
	transactionIndex uint32
	hasIndex         bool
	affectedAccounts []string
	parseErr         error
}

// AcceptedTransactionProjection is an owned snapshot of a valid accepted
// transaction and its publication fields.
type AcceptedTransactionProjection struct {
	Transaction      map[string]any
	Metadata         map[string]any
	Result           ter.Result
	TransactionIndex uint32
	AffectedAccounts []string
}

// ParseAcceptedTransaction parses both fields of an accepted transaction leaf.
// Each serialized component is decoded exactly once, even when the other
// component is malformed.
func ParseAcceptedTransaction(data []byte) *AcceptedTransaction {
	accepted := &AcceptedTransaction{
		raw:    append([]byte(nil), data...),
		result: ter.TemMALFORMED,
	}
	txBlob, metaBlob, err := txcore.SplitTxWithMetaBlobStrict(accepted.raw)
	if err != nil {
		accepted.parseErr = fmt.Errorf("split accepted transaction: %w", err)
		return accepted
	}
	accepted.transactionBlob = txBlob
	accepted.metadataBlob = metaBlob

	var parseErrors []error
	txMap, txErr := binarycodec.DecodeBytes(txBlob)
	if txErr != nil {
		parseErrors = append(parseErrors, fmt.Errorf("decode accepted transaction: %w", txErr))
	} else {
		accepted.transaction = txMap
		if err := validateAcceptedTransaction(txBlob, txMap); err != nil {
			parseErrors = append(parseErrors, fmt.Errorf("validate accepted transaction: %w", err))
		}
	}

	metaMap, metaErr := binarycodec.DecodeBytes(metaBlob)
	if metaErr != nil {
		parseErrors = append(parseErrors, fmt.Errorf("decode accepted metadata: %w", metaErr))
	} else {
		accepted.metadata = metaMap
		accepted.captureMetadata(metaMap)
		if err := validateAcceptedMetadata(metaMap); err != nil {
			parseErrors = append(parseErrors, fmt.Errorf("validate accepted metadata: %w", err))
		}
	}

	accepted.parseErr = errors.Join(parseErrors...)
	if accepted.parseErr != nil && accepted.result == ter.TesSUCCESS {
		accepted.result = ter.TemMALFORMED
	}
	return accepted
}

func validateAcceptedTransaction(blob []byte, fields map[string]any) error {
	if len(blob) < minSerializedTransactionBytes || len(blob) > maxSerializedTransactionBytes {
		return errors.New("transaction length invalid")
	}
	typeName, _ := fields["TransactionType"].(string)
	txType, ok := txcore.TypeFromName(typeName)
	if !ok {
		return fmt.Errorf("invalid transaction type %q", typeName)
	}
	return txcore.ValidateTemplateFields(txType, fields)
}

func validateAcceptedMetadata(fields map[string]any) error {
	for _, name := range []string{"TransactionResult", "TransactionIndex", "AffectedNodes"} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("metadata is missing %s", name)
		}
	}
	if _, ok := fields["TransactionIndex"].(uint32); !ok {
		return errors.New("metadata has invalid TransactionIndex")
	}
	if _, ok := fields["AffectedNodes"].([]any); !ok {
		return errors.New("metadata has invalid AffectedNodes")
	}
	name, ok := fields["TransactionResult"].(string)
	if !ok || name == "" {
		return errors.New("metadata has invalid TransactionResult")
	}
	if _, err := definitions.Get().TransactionResultCode(name); err != nil {
		return fmt.Errorf("unknown transaction result %q: %w", name, err)
	}
	return nil
}

func (a *AcceptedTransaction) captureMetadata(fields map[string]any) {
	if index, ok := fields["TransactionIndex"].(uint32); ok {
		a.transactionIndex = index
		a.hasIndex = true
	}
	if name, ok := fields["TransactionResult"].(string); ok && name != "" {
		if code, err := definitions.Get().TransactionResultCode(name); err == nil {
			a.result = ter.Result(code)
		}
	}
	a.affectedAccounts = affectedAccountsFromMetadata(fields)
}

func (a *AcceptedTransaction) Raw() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.raw...)
}

func (a *AcceptedTransaction) TransactionBlob() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.transactionBlob...)
}

func (a *AcceptedTransaction) MetadataBlob() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.metadataBlob...)
}

func (a *AcceptedTransaction) Transaction() map[string]any {
	if err := a.ParseError(); err != nil {
		return nil
	}
	return cloneAcceptedMap(a.transaction)
}

func (a *AcceptedTransaction) Metadata() map[string]any {
	if err := a.ParseError(); err != nil {
		return nil
	}
	return cloneAcceptedMap(a.metadata)
}

func (a *AcceptedTransaction) Result() ter.Result {
	if a == nil || a.parseErr != nil || a.transaction == nil || a.metadata == nil {
		return ter.TemMALFORMED
	}
	return a.result
}

func (a *AcceptedTransaction) TransactionIndex() (uint32, bool) {
	if a == nil {
		return 0, false
	}
	return a.transactionIndex, a.hasIndex
}

func (a *AcceptedTransaction) AffectedAccounts() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.affectedAccounts...)
}

func (a *AcceptedTransaction) ParseError() error {
	if a == nil {
		return errors.New("nil accepted transaction")
	}
	if a.parseErr == nil && (a.transaction == nil || a.metadata == nil) {
		return errors.New("uninitialized accepted transaction")
	}
	return a.parseErr
}

// Projection returns one deep-owned publication snapshot. Invalid values return
// the same parse error retained by the accepted transaction and never expose a
// successful result.
func (a *AcceptedTransaction) Projection() (AcceptedTransactionProjection, error) {
	projection := AcceptedTransactionProjection{Result: ter.TemMALFORMED}
	if err := a.ParseError(); err != nil {
		return projection, err
	}
	projection.Transaction = cloneAcceptedMap(a.transaction)
	projection.Metadata = cloneAcceptedMap(a.metadata)
	projection.Result = a.result
	projection.TransactionIndex = a.transactionIndex
	projection.AffectedAccounts = append([]string(nil), a.affectedAccounts...)
	return projection, nil
}

func cloneAcceptedMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneAcceptedValue(value)
	}
	return cloned
}

func cloneAcceptedValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneAcceptedMap(value)
	case []any:
		cloned := make([]any, len(value))
		for i := range value {
			cloned[i] = cloneAcceptedValue(value[i])
		}
		return cloned
	case []byte:
		return append([]byte(nil), value...)
	default:
		return value
	}
}

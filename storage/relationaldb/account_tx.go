package relationaldb

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// BuildAccountTxPage applies account_tx pagination semantics to the bounded
// rows returned by a relational backend.
func BuildAccountTxPage(opName string, options AccountTxPageOptions, scanned []TransactionInfo) (*AccountTxResult, error) {
	result := &AccountTxResult{
		LedgerRange: LedgerRange{Min: options.MinLedger, Max: options.MaxLedger},
		Limit:       options.Limit,
	}

	if options.Delegate == nil {
		result.Transactions = scanned
		if uint64(len(scanned)) > uint64(options.Limit) {
			result.Transactions = scanned[:options.Limit]
			if len(result.Transactions) > 0 {
				last := result.Transactions[len(result.Transactions)-1]
				result.Marker = accountTxMarker(last)
			}
		}
		return result, nil
	}

	fetchedRows := len(scanned)
	lastScanned := accountTxMarkerFromRows(scanned)
	if options.Marker != nil {
		markerIndex := -1
		for index, transaction := range scanned {
			if transaction.LedgerSeq == options.Marker.LedgerSeq && transaction.TxnSeq == options.Marker.TxnSeq {
				markerIndex = index
				break
			}
		}
		if markerIndex < 0 {
			return result, nil
		}
		scanned = scanned[markerIndex+1:]
	}

	result.Transactions = make([]TransactionInfo, 0, min(len(scanned), int(options.Limit)))
	var lastEmitted *AccountTxMarker
	for _, transaction := range scanned {
		matches, err := matchesAccountTxDelegate(transaction.RawTxn, options.Account, *options.Delegate)
		if err != nil {
			return nil, NewDataError(opName, fmt.Sprintf("malformed transaction while applying delegate filter: %v", err), ErrInvalidData)
		}
		if !matches {
			continue
		}
		if uint64(len(result.Transactions)) == uint64(options.Limit) {
			result.Marker = lastEmitted
			break
		}
		result.Transactions = append(result.Transactions, transaction)
		lastEmitted = accountTxMarker(transaction)
	}

	if result.Marker == nil && uint64(fetchedRows) == uint64(options.Limit)+1 {
		result.Marker = lastScanned
	}
	return result, nil
}

func accountTxMarker(transaction TransactionInfo) *AccountTxMarker {
	return &AccountTxMarker{LedgerSeq: transaction.LedgerSeq, TxnSeq: transaction.TxnSeq}
}

func accountTxMarkerFromRows(transactions []TransactionInfo) *AccountTxMarker {
	if len(transactions) == 0 {
		return nil
	}
	return accountTxMarker(transactions[len(transactions)-1])
}

func matchesAccountTxDelegate(rawTransaction []byte, account AccountID, filter AccountTxDelegateFilter) (bool, error) {
	if len(rawTransaction) == 0 {
		return false, nil
	}
	transaction, err := binarycodec.DecodeBytes(rawTransaction)
	if err != nil {
		return false, err
	}
	owner, err := accountTxAccountID(transaction["Account"], "Account")
	if err != nil {
		return false, err
	}
	delegateValue, present := transaction["Delegate"]
	if !present {
		return false, nil
	}
	delegate, err := accountTxAccountID(delegateValue, "Delegate")
	if err != nil {
		return false, err
	}

	switch filter.Role {
	case AccountTxDelegateActor:
		return owner == account && delegate != account &&
			(filter.Counterparty == nil || delegate == *filter.Counterparty), nil
	case AccountTxDelegateAuthorizer:
		return delegate == account && owner != account &&
			(filter.Counterparty == nil || owner == *filter.Counterparty), nil
	default:
		return false, fmt.Errorf("invalid delegate role %d", filter.Role)
	}
}

func accountTxAccountID(value any, field string) (AccountID, error) {
	address, ok := value.(string)
	if !ok {
		return AccountID{}, fmt.Errorf("%s is not an account", field)
	}
	_, raw, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return AccountID{}, fmt.Errorf("decode %s: %w", field, err)
	}
	if len(raw) != len(AccountID{}) {
		return AccountID{}, fmt.Errorf("decode %s: account ID has length %d", field, len(raw))
	}
	var account AccountID
	copy(account[:], raw)
	return account, nil
}

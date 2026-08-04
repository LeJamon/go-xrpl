package txq

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func canonicalSubmission(txn tx.Transaction, suppliedID [32]byte, suppliedAccount [20]byte) (tx.Transaction, []byte, [32]byte, [20]byte, error) {
	var zeroID [32]byte
	var zeroAccount [20]byte
	if txn == nil || txn.GetCommon() == nil {
		return nil, nil, zeroID, zeroAccount, fmt.Errorf("nil transaction")
	}

	raw := append([]byte(nil), txn.GetRawBytes()...)
	if len(raw) == 0 {
		serialized, err := tx.SerializeTransaction(txn)
		if err != nil {
			return nil, nil, zeroID, zeroAccount, err
		}
		raw = append([]byte(nil), serialized...)
	}
	parsed, err := tx.ParseFromBinary(raw)
	if err != nil {
		return nil, nil, zeroID, zeroAccount, err
	}

	id, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		return nil, nil, zeroID, zeroAccount, err
	}
	account, err := state.DecodeAccountID(parsed.GetCommon().Account)
	if err != nil {
		return nil, nil, zeroID, zeroAccount, err
	}
	if suppliedAccount != zeroAccount && suppliedAccount != account {
		return nil, nil, zeroID, zeroAccount, fmt.Errorf("transaction account does not match supplied account")
	}
	if suppliedID != zeroID && suppliedID != id {
		return nil, nil, zeroID, zeroAccount, fmt.Errorf("transaction id does not match supplied id")
	}
	return parsed, raw, id, account, nil
}

type syntheticTransaction interface {
	tx.Transaction
	txqSynthetic()
}

func candidateTransaction(c *candidate) tx.Transaction {
	if c == nil {
		return nil
	}
	if len(c.blob) >= 32 {
		if parsed, err := tx.ParseFromBinary(c.blob); err == nil {
			return parsed
		}
	}
	return c.Txn
}

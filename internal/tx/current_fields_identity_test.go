package tx

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

type identityTestTransaction struct {
	Transaction
	flattenErr error
	calls      int
	fields     map[string]any
}

func (tx *identityTestTransaction) Flatten() (map[string]any, error) {
	tx.calls++
	if tx.flattenErr != nil {
		return nil, tx.flattenErr
	}
	if tx.fields != nil {
		return tx.fields, nil
	}
	return tx.Transaction.Flatten()
}

func TestCurrentFieldsIdentity(t *testing.T) {
	failure := errors.New("cannot flatten transaction")
	for _, test := range []struct {
		name                      string
		raw, bound, mutated, fail bool
		wantMatch                 bool
		wantCalls                 int
	}{
		{name: "programmatic", wantMatch: true, wantCalls: 1},
		{name: "programmatic failure", fail: true, wantMatch: true, wantCalls: 1},
		{name: "bound", raw: true, bound: true, wantMatch: true, wantCalls: 1},
		{name: "mutated", raw: true, bound: true, mutated: true, wantCalls: 1},
		{name: "bound failure", raw: true, bound: true, fail: true, wantCalls: 1},
		{name: "unbound", raw: true},
		{name: "unbound failure", raw: true, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := NewBaseTx(TypeAccountSet, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
			base.Fee = "10"
			base.SetSequence(1)
			originalID, err := ComputeCurrentTransactionHash(base)
			require.NoError(t, err)
			if test.raw {
				raw, err := SerializeTransaction(base)
				require.NoError(t, err)
				if test.bound {
					require.NoError(t, BindRawBytes(base, raw))
				} else {
					base.SetRawBytes(raw)
				}
			}
			if test.mutated {
				base.Fee = "20"
			}
			txn := &identityTestTransaction{Transaction: base}
			if test.fail {
				txn.flattenErr = failure
			}
			id, matches, err := CurrentFieldsIdentity(txn)
			require.Equal(t, test.wantMatch, matches)
			require.Equal(t, test.wantCalls, txn.calls)
			if test.fail && test.wantCalls != 0 {
				require.ErrorIs(t, err, failure)
				require.Zero(t, id)
			} else {
				require.NoError(t, err)
				switch {
				case test.wantCalls == 0:
					require.Zero(t, id)
				case test.mutated:
					require.NotEqual(t, originalID, id)
				default:
					require.Equal(t, originalID, id)
				}
			}
			txn.calls = 0
			matches, err = CurrentFieldsMatchRaw(txn)
			require.Equal(t, test.wantMatch, matches)
			if !test.raw || !test.bound {
				require.NoError(t, err)
				require.Zero(t, txn.calls)
			} else if test.fail {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSerializeCurrentTransactionMatchesHexEncoding(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	baseFields := func() map[string]any {
		return map[string]any{"TransactionType": "AccountSet", "Account": account, "Sequence": uint32(1), "Fee": "10", "SigningPubKey": "", "TxnSignature": "ABCD"}
	}
	tests := []struct {
		name      string
		modify    func(map[string]any)
		wantError bool
	}{
		{name: "ordinary"},
		{name: "explicit empty memo", modify: func(m map[string]any) {
			m["Memos"] = []any{map[string]any{"Memo": map[string]any{"MemoData": ""}}}
		}},
		{name: "multisigned", modify: func(m map[string]any) {
			delete(m, "TxnSignature")
			m["Signers"] = []any{map[string]any{"Signer": map[string]any{"Account": account, "SigningPubKey": "ABCD", "TxnSignature": "ABCD"}}}
		}},
		{name: "batch", modify: func(m map[string]any) {
			m["TransactionType"] = "Batch"
			m["Flags"] = uint32(0x00010000)
			inners := make([]any, 2)
			for i := range 2 {
				inner := baseFields()
				inner["Sequence"] = uint32(i + 1)
				inner["Fee"] = "0"
				inner["Flags"] = TfInnerBatchTxn
				delete(inner, "TxnSignature")
				inners[i] = map[string]any{"RawTransaction": inner}
			}
			m["RawTransactions"] = inners
			m["BatchSigners"] = []any{map[string]any{"BatchSigner": map[string]any{"Account": account, "SigningPubKey": "ABCD", "TxnSignature": "ABCD"}}}
		}},
		{name: "role signatures", modify: func(m map[string]any) {
			m["CounterpartySignature"] = map[string]any{"SigningPubKey": "ABCD", "TxnSignature": "ABCD"}
			m["SponsorSignature"] = map[string]any{"SigningPubKey": "ABCD", "TxnSignature": "ABCD"}
		}},
		{name: "unknown field", wantError: true, modify: func(m map[string]any) { m["UnknownField"] = true }},
		{name: "invalid hex", wantError: true, modify: func(m map[string]any) { m["TxnSignature"] = "XYZ" }},
		{name: "array limit", wantError: true, modify: func(m map[string]any) {
			memos := make([]any, 513)
			for i := range memos {
				memos[i] = map[string]any{"Memo": map[string]any{"MemoData": "AA"}}
			}
			m["Memos"] = memos
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := baseFields()
			if test.modify != nil {
				test.modify(fields)
			}
			txn := &identityTestTransaction{Transaction: NewBaseTx(TypeAccountSet, account), fields: fields}
			encoded, oldErr := binarycodec.Encode(fields)
			actual, err := serializeCurrentTransaction(txn)
			if test.wantError {
				require.Error(t, oldErr)
				require.EqualError(t, err, oldErr.Error())
				return
			}
			require.NoError(t, oldErr)
			require.NoError(t, err)
			expected, err := hex.DecodeString(encoded)
			require.NoError(t, err)
			require.Equal(t, expected, actual)
			actualID, err := ComputeCurrentTransactionHash(txn)
			require.NoError(t, err)
			require.Equal(t, hashWithTxnPrefix(expected), actualID)
		})
	}
}

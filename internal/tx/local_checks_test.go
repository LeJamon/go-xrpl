package tx

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func memoCommon(memos ...Memo) *Common {
	wrapped := make([]MemoWrapper, len(memos))
	for i, m := range memos {
		wrapped[i] = MemoWrapper{Memo: m}
	}
	return &Common{Account: "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS", Memos: wrapped}
}

// TestPassesLocalChecks_MemoSize pins the rippled isMemoOkay boundary: the whole
// Memos array serialized (headers included, per-object end markers, but not the
// sfMemos field header or array end marker) must be <= 1024 bytes. A MemoData of
// 1019 bytes serializes to exactly 1024; 1020 bytes to 1025.
func TestPassesLocalChecks_MemoSize(t *testing.T) {
	tests := []struct {
		name      string
		dataBytes int
		want      ter.Result
	}{
		{"at the 1024-byte boundary", 1019, ter.TesSUCCESS},
		{"one byte over the boundary", 1020, ter.TemMALFORMED},
		{"well under the boundary", 100, ter.TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common := memoCommon(Memo{MemoData: strings.Repeat("AA", tt.dataBytes)})
			if got := PassesLocalChecks(common); got != tt.want {
				t.Fatalf("PassesLocalChecks = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPassesLocalChecks_MemoFields pins the per-field hex and charset rules:
// every field must be valid hex, and the decoded MemoType/MemoFormat bytes must
// be RFC 3986 URL characters — MemoData is unrestricted. There are no per-field
// size caps.
func TestPassesLocalChecks_MemoFields(t *testing.T) {
	tests := []struct {
		name string
		memo Memo
		want ter.Result
	}{
		{"no memos", Memo{}, ter.TesSUCCESS},
		{"valid hex fields", Memo{MemoType: "41", MemoData: "42", MemoFormat: "43"}, ter.TesSUCCESS},
		{"MemoType not hex", Memo{MemoType: "GG"}, ter.TemMALFORMED},
		{"MemoData not hex", Memo{MemoData: "XY"}, ter.TemMALFORMED},
		{"MemoFormat not hex", Memo{MemoFormat: "ZZ"}, ter.TemMALFORMED},
		// 0x01 is a valid byte but not an RFC 3986 URL character.
		{"MemoType non-URL char", Memo{MemoType: "01"}, ter.TemMALFORMED},
		{"MemoFormat non-URL char", Memo{MemoFormat: "01"}, ter.TemMALFORMED},
		// MemoData may hold arbitrary bytes, including non-URL characters.
		{"MemoData non-URL char", Memo{MemoData: "01"}, ter.TesSUCCESS},
		// No per-field cap: a MemoType larger than the retired 256-byte cap is
		// fine as long as the whole array fits in 1024 serialized bytes and it is
		// valid hex of URL characters ('A' = 0x41).
		{"large MemoType within array budget", Memo{MemoType: strings.Repeat("41", 300)}, ter.TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common := memoCommon(tt.memo)
			if got := PassesLocalChecks(common); got != tt.want {
				t.Fatalf("PassesLocalChecks = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPassesLocalChecks_NoMemos confirms a transaction without memos passes.
func TestPassesLocalChecks_NoMemos(t *testing.T) {
	if got := PassesLocalChecks(&Common{Account: "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"}); got != ter.TesSUCCESS {
		t.Fatalf("PassesLocalChecks(no memos) = %v, want TesSUCCESS", got)
	}
}

func TestLocalChecksFailureReason(t *testing.T) {
	tests := []struct {
		name string
		memo Memo
		want string
	}{
		{
			name: "oversized",
			memo: Memo{MemoData: strings.Repeat("AA", 1020)},
			want: "The memo exceeds the maximum allowed size.",
		},
		{
			name: "invalid hex",
			memo: Memo{MemoData: "GG"},
			want: "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data.",
		},
		{
			name: "invalid URL character",
			memo: Memo{MemoType: "01"},
			want: "The MemoType and MemoFormat fields may only contain characters that are allowed in URLs under RFC 3986.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := LocalChecksFailureReason(memoCommon(test.memo)); got != test.want {
				t.Fatalf("LocalChecksFailureReason = %q, want %q", got, test.want)
			}
		})
	}
}

type localChecksTransaction struct {
	BaseTx
	fields map[string]any
}

func newLocalChecksTransaction(txType Type, fields map[string]any) *localChecksTransaction {
	return &localChecksTransaction{BaseTx: *NewBaseTx(txType, "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"), fields: fields}
}

func (transaction *localChecksTransaction) Flatten() (map[string]any, error) {
	return transaction.fields, nil
}

type localChecksBatch struct {
	*localChecksTransaction
	inners  []Transaction
	signers []BatchSignerInfo
}

func (batch *localChecksBatch) InnerTransactions() []Transaction {
	return batch.inners
}

func (batch *localChecksBatch) GetBatchSigners() []BatchSignerInfo {
	return batch.signers
}

func TestTransactionLocalChecksFailureReason(t *testing.T) {
	mpt := map[string]any{"value": "1", "mpt_issuance_id": strings.Repeat("A", 48)}
	zeroAccount := strings.Repeat("0", 40)

	tests := []struct {
		name        string
		transaction Transaction
		want        string
	}{
		{
			name:        "default account field",
			transaction: newLocalChecksTransaction(TypePayment, map[string]any{"Destination": zeroAccount}),
			want:        "An account field is invalid.",
		},
		{
			name:        "zero length account field",
			transaction: newLocalChecksTransaction(TypePayment, map[string]any{"Destination": ""}),
			want:        "An account field is invalid.",
		},
		{
			name:        "pseudo transaction",
			transaction: newLocalChecksTransaction(TypeFee, map[string]any{}),
			want:        "Cannot submit pseudo transactions.",
		},
		{
			name:        "MPT supported field",
			transaction: newLocalChecksTransaction(TypePayment, map[string]any{"Amount": mpt}),
		},
		{
			name:        "MPT unsupported common fee",
			transaction: newLocalChecksTransaction(TypePayment, map[string]any{"Fee": mpt}),
			want:        "Amount can not be MPT.",
		},
		{
			name:        "MPT unsupported transaction field",
			transaction: newLocalChecksTransaction(TypePaymentChannelCreate, map[string]any{"Amount": mpt}),
			want:        "Amount can not be MPT.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TransactionLocalChecksFailureReason(test.transaction); got != test.want {
				t.Fatalf("TransactionLocalChecksFailureReason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTransactionMapLocalChecksBatchInnerType(t *testing.T) {
	tests := []struct {
		name      string
		innerType any
		want      string
	}{
		{
			name: "missing",
			want: "Field not found: TransactionType",
		},
		{
			name:      "unknown",
			innerType: uint16(65535),
			want:      "Invalid transaction type 65535",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inner := map[string]any{}
			if test.innerType != nil {
				inner["TransactionType"] = test.innerType
			}
			fields := map[string]any{
				"RawTransactions": []any{map[string]any{"RawTransaction": inner}},
			}
			if got := TransactionMapLocalChecksFailureReason(TypeBatch, fields); got != test.want {
				t.Fatalf("TransactionMapLocalChecksFailureReason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTransactionMapLocalChecksCountsPresentEmptyMemoFields(t *testing.T) {
	fields := map[string]any{
		"Memos": []any{map[string]any{"Memo": map[string]any{
			"MemoData": strings.Repeat("AA", 1019),
			"MemoType": "",
		}}},
	}
	if got := TransactionMapLocalChecksFailureReason(TypePayment, fields); got != "The memo exceeds the maximum allowed size." {
		t.Fatalf("TransactionMapLocalChecksFailureReason = %q, want memo size rejection", got)
	}
}

func TestTransactionLocalChecksBatchLimits(t *testing.T) {
	inner := newLocalChecksTransaction(TypePayment, map[string]any{})
	nested := newLocalChecksTransaction(TypeBatch, map[string]any{})
	tests := []struct {
		name  string
		batch *localChecksBatch
		want  string
	}{
		{
			name: "batch signers limit",
			batch: &localChecksBatch{
				localChecksTransaction: newLocalChecksTransaction(TypeBatch, map[string]any{}),
				signers:                make([]BatchSignerInfo, 9),
			},
			want: "Batch Signers array exceeds max entries.",
		},
		{
			name: "raw transactions limit",
			batch: &localChecksBatch{
				localChecksTransaction: newLocalChecksTransaction(TypeBatch, map[string]any{}),
				inners:                 make([]Transaction, 9),
			},
			want: "Raw Transactions array exceeds max entries.",
		},
		{
			name: "nested batch",
			batch: &localChecksBatch{
				localChecksTransaction: newLocalChecksTransaction(TypeBatch, map[string]any{}),
				inners:                 []Transaction{inner, nested},
			},
			want: "Raw Transactions may not contain batch transactions.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TransactionLocalChecksFailureReason(test.batch); got != test.want {
				t.Fatalf("TransactionLocalChecksFailureReason = %q, want %q", got, test.want)
			}
		})
	}
}

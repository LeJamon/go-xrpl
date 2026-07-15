package grpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"

	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
)

// fakeLookup is a tiny LedgerLookup that returns canned ledgers by hash
// and sequence. Each ledger may include state objects and transactions.
type fakeLookup struct {
	bySeq      map[uint32]*ledger.Ledger
	byHash     map[[32]byte]*ledger.Ledger
	validated  *ledger.Ledger
	closed     *ledger.Ledger
	openLedger *ledger.Ledger
	entryErr   error
	entryIndex string
}

type networkLookup struct {
	*fakeLookup
	standalone   bool
	validatedAge time.Duration
}

func (l *networkLookup) IsStandalone() bool                   { return l.standalone }
func (l *networkLookup) GetValidatedLedgerAge() time.Duration { return l.validatedAge }

func (f *fakeLookup) IsStandalone() bool                   { return true }
func (f *fakeLookup) GetValidatedLedgerAge() time.Duration { return 0 }

func (f *fakeLookup) GetLedgerByHash(h [32]byte) (*ledger.Ledger, error) {
	if l, ok := f.byHash[h]; ok {
		return l, nil
	}
	for _, l := range f.bySeq {
		if l.Hash() == h {
			return l, nil
		}
	}
	return nil, svcerr.ErrLedgerNotFound
}
func (f *fakeLookup) GetLedgerBySequence(seq uint32) (*ledger.Ledger, error) {
	if l, ok := f.bySeq[seq]; ok {
		return l, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeLookup) GetClosedLedger() *ledger.Ledger    { return f.closed }
func (f *fakeLookup) GetValidatedLedger() *ledger.Ledger { return f.validated }
func (f *fakeLookup) GetOpenLedger() *ledger.Ledger      { return f.openLedger }
func (f *fakeLookup) GetLedgerEntry(_ context.Context, key [32]byte, ledgerIndex string) (*service.LedgerEntryResult, error) {
	f.entryIndex = ledgerIndex
	if f.entryErr != nil {
		return nil, f.entryErr
	}
	if f.validated == nil {
		return nil, svcerr.ErrLedgerEntryNotFound
	}
	k := keylet.Keylet{Key: key}
	exists, _ := f.validated.Exists(k)
	if !exists {
		return nil, svcerr.ErrLedgerEntryNotFound
	}
	data, err := f.validated.Read(k)
	if err != nil {
		return nil, svcerr.ErrLedgerEntryNotFound
	}
	return &service.LedgerEntryResult{
		LedgerIndex: f.validated.Sequence(),
		LedgerHash:  f.validated.Hash(),
		Node:        data,
		Validated:   true,
	}, nil
}

// pad right-pads a string with zero bytes to at least n bytes; the
// SHAMap layer rejects items smaller than 12 bytes.
func pad(s string, n int) []byte {
	out := make([]byte, n)
	copy(out, s)
	return out
}

// txWithMeta frames a transaction blob and its metadata into the combined
// [VL-len][tx][VL-len][meta] form the tx map stores, which GetLedger's expand
// path splits back apart via tx.SplitTxWithMetaBlob. Both lengths stay below
// 193, so each variable-length prefix is a single byte.
func txWithMeta(txBytes, metaBytes []byte) []byte {
	out := make([]byte, 0, len(txBytes)+len(metaBytes)+2)
	out = append(out, byte(len(txBytes)))
	out = append(out, txBytes...)
	out = append(out, byte(len(metaBytes)))
	out = append(out, metaBytes...)
	return out
}

// newTestLedger builds an immediately-validated test ledger at the given
// sequence with the supplied state entries (key→data) and transactions
// (txHash→blob).
func newTestLedger(t *testing.T, seq uint32, state map[[32]byte][]byte, txs map[[32]byte][]byte) *ledger.Ledger {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	for k, data := range state {
		if err := stateMap.Put(k, data); err != nil {
			t.Fatalf("state Put: %v", err)
		}
	}
	txMap := shamap.New(shamap.TypeTransaction)
	for k, data := range txs {
		if err := txMap.Put(k, data); err != nil {
			t.Fatalf("tx Put: %v", err)
		}
	}

	hdr := header.LedgerHeader{
		LedgerIndex:         seq,
		Drops:               100_000_000_000_000,
		CloseTime:           time.Unix(1_700_000_000, 0).UTC(),
		ParentCloseTime:     time.Unix(1_699_999_990, 0).UTC(),
		CloseTimeResolution: 10,
		Validated:           true,
		Accepted:            true,
	}
	if seq > 1 {
		hdr.ParentHash = [32]byte{byte(seq - 1), 0xAB}
	}
	hdr.Hash = [32]byte{byte(seq), 0xAB}
	return ledger.FromGenesis(hdr, stateMap, txMap, drops.Fees{})
}

func newClosedTestLedger(t *testing.T, parent *ledger.Ledger) *ledger.Ledger {
	t.Helper()
	l, err := ledger.NewOpen(parent, time.Now())
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	if err := l.Close(time.Now(), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return l
}

func ledgerShortcutSpec(shortcut rpcv1.LedgerSpecifier_Shortcut) *rpcv1.LedgerSpecifier {
	return &rpcv1.LedgerSpecifier{
		Ledger: &rpcv1.LedgerSpecifier_Shortcut_{Shortcut: shortcut},
	}
}

func ledgerSequenceSpec(sequence uint32) *rpcv1.LedgerSpecifier {
	return &rpcv1.LedgerSpecifier{
		Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: sequence},
	}
}

func ledgerHashSpec(hash []byte) *rpcv1.LedgerSpecifier {
	return &rpcv1.LedgerSpecifier{
		Ledger: &rpcv1.LedgerSpecifier_Hash{Hash: hash},
	}
}

func TestResolveLedgerSelector(t *testing.T) {
	open := newTestLedger(t, 10, nil, nil)
	closed := newTestLedger(t, 20, nil, nil)
	validated := newTestLedger(t, 30, nil, nil)
	exact := newTestLedger(t, 40, nil, nil)
	exactHash := exact.Hash()
	srv := NewServer(&fakeLookup{
		openLedger: open,
		closed:     closed,
		validated:  validated,
		bySeq:      map[uint32]*ledger.Ledger{exact.Sequence(): exact},
		byHash:     map[[32]byte]*ledger.Ledger{exactHash: exact},
	})

	tests := []struct {
		name string
		spec *rpcv1.LedgerSpecifier
		want *ledger.Ledger
	}{
		{name: "nil defaults current", want: open},
		{name: "empty defaults current", spec: &rpcv1.LedgerSpecifier{}, want: open},
		{name: "unspecified defaults current", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_UNSPECIFIED), want: open},
		{name: "current", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CURRENT), want: open},
		{name: "closed", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CLOSED), want: closed},
		{name: "validated", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED), want: validated},
		{name: "sequence", spec: ledgerSequenceSpec(exact.Sequence()), want: exact},
		{name: "hash", spec: ledgerHashSpec(exactHash[:]), want: exact},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := srv.resolveLedger(test.spec)
			if err != nil {
				t.Fatalf("resolveLedger() returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("resolveLedger() = ledger %v, want ledger %d", got, test.want.Sequence())
			}
		})
	}
}

func TestResolveLedgerSelectorErrors(t *testing.T) {
	missingHash := [32]byte{0xff}
	srv := NewServer(&fakeLookup{})
	tests := []struct {
		name    string
		spec    *rpcv1.LedgerSpecifier
		code    codes.Code
		message string
	}{
		{name: "missing default current", code: codes.NotFound, message: "notSynced"},
		{
			name: "missing explicit current",
			spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CURRENT),
			code: codes.NotFound, message: "notSynced",
		},
		{
			name: "missing closed",
			spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CLOSED),
			code: codes.NotFound, message: "notSynced",
		},
		{
			name: "missing validated",
			spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED),
			code: codes.NotFound, message: "notSynced",
		},
		{
			name: "missing sequence",
			spec: ledgerSequenceSpec(999),
			code: codes.NotFound, message: "ledgerNotFound",
		},
		{
			name: "missing hash",
			spec: ledgerHashSpec(missingHash[:]),
			code: codes.NotFound, message: "ledgerNotFound",
		},
		{
			name: "malformed hash",
			spec: ledgerHashSpec([]byte("short")),
			code: codes.InvalidArgument, message: "ledgerHashMalformed",
		},
		{
			name: "unknown shortcut",
			spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_Shortcut(99)),
			code: codes.InvalidArgument, message: "unknown ledger shortcut 99",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := srv.resolveLedger(test.spec)
			if got != nil {
				t.Fatalf("resolveLedger() returned ledger %d with error %v", got.Sequence(), err)
			}
			if status.Code(err) != test.code {
				t.Fatalf("resolveLedger() code = %s, want %s: %v", status.Code(err), test.code, err)
			}
			if message := status.Convert(err).Message(); message != test.message {
				t.Fatalf("resolveLedger() message = %q, want %q", message, test.message)
			}
		})
	}
}

func TestResolveLedgerSelectorRejectsStaleNetworkState(t *testing.T) {
	validated := newTestLedger(t, 10, nil, nil)
	newer := newTestLedger(t, 11, nil, nil)
	lookup := &networkLookup{fakeLookup: &fakeLookup{
		validated:  validated,
		closed:     validated,
		openLedger: newer,
		bySeq: map[uint32]*ledger.Ledger{
			validated.Sequence(): validated,
			newer.Sequence():     newer,
		},
		byHash: map[[32]byte]*ledger.Ledger{validated.Hash(): validated},
	}, validatedAge: maxValidatedLedgerAge + time.Second}
	srv := NewServer(lookup)

	for _, spec := range []*rpcv1.LedgerSpecifier{
		nil,
		ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CLOSED),
		ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED),
		ledgerSequenceSpec(newer.Sequence()),
	} {
		if got, err := srv.resolveLedger(spec); got != nil || status.Code(err) != codes.NotFound || status.Convert(err).Message() != "notSynced" {
			t.Errorf("resolveLedger(%v) = (%v, %v), want NotFound notSynced", spec, got, err)
		}
	}
	if got, err := srv.resolveLedger(ledgerSequenceSpec(validated.Sequence())); err != nil || got != validated {
		t.Fatalf("stale exact validated ledger = (%v, %v), want ledger %d", got, err, validated.Sequence())
	}
	validatedHash := validated.Hash()
	if got, err := srv.resolveLedger(ledgerHashSpec(validatedHash[:])); err != nil || got != validated {
		t.Fatalf("stale hash ledger = (%v, %v), want ledger %d", got, err, validated.Sequence())
	}

	lookup.validatedAge = maxValidatedLedgerAge
	if got, err := srv.resolveLedger(nil); err != nil || got != newer {
		t.Fatalf("current at freshness boundary = (%v, %v), want ledger %d", got, err, newer.Sequence())
	}
	lookup.validatedAge += time.Nanosecond
	if got, err := srv.resolveLedger(nil); got != nil || status.Code(err) != codes.NotFound || status.Convert(err).Message() != "notSynced" {
		t.Fatalf("current beyond freshness boundary = (%v, %v), want NotFound notSynced", got, err)
	}

	lookup.standalone = true
	if got, err := srv.resolveLedger(nil); err != nil || got != newer {
		t.Fatalf("standalone current = (%v, %v), want ledger %d", got, err, newer.Sequence())
	}
}

func TestResolveLedgerSelectorRejectsLaggingShortcut(t *testing.T) {
	lagging := newTestLedger(t, 10, nil, nil)
	validated := newTestLedger(t, 21, nil, nil)
	lookup := &networkLookup{fakeLookup: &fakeLookup{
		validated:  validated,
		closed:     lagging,
		openLedger: lagging,
	}}
	srv := NewServer(lookup)

	for _, spec := range []*rpcv1.LedgerSpecifier{
		nil,
		ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CLOSED),
	} {
		if got, err := srv.resolveLedger(spec); got != nil || status.Code(err) != codes.NotFound || status.Convert(err).Message() != "notSynced" {
			t.Errorf("resolveLedger(%v) = (%v, %v), want NotFound notSynced", spec, got, err)
		}
	}
	if got, err := srv.resolveLedger(ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED)); err != nil || got != validated {
		t.Fatalf("validated shortcut = (%v, %v), want ledger %d", got, err, validated.Sequence())
	}
}

func TestSpecToIndexSelectorParity(t *testing.T) {
	exact := newTestLedger(t, 40, nil, nil)
	exactHash := exact.Hash()
	srv := NewServer(&fakeLookup{byHash: map[[32]byte]*ledger.Ledger{exactHash: exact}})

	tests := []struct {
		name string
		spec *rpcv1.LedgerSpecifier
		want string
	}{
		{name: "nil defaults current", want: "current"},
		{name: "empty defaults current", spec: &rpcv1.LedgerSpecifier{}, want: "current"},
		{name: "unspecified defaults current", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_UNSPECIFIED), want: "current"},
		{name: "current", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CURRENT), want: "current"},
		{name: "closed", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_CLOSED), want: "closed"},
		{name: "validated", spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED), want: "validated"},
		{name: "sequence", spec: ledgerSequenceSpec(math.MaxUint32), want: "4294967295"},
		{name: "hash resolves exact sequence", spec: ledgerHashSpec(exactHash[:]), want: "40"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := srv.specToIndex(test.spec)
			if err != nil {
				t.Fatalf("specToIndex() returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("specToIndex() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSpecToIndexSelectorErrors(t *testing.T) {
	missingHash := [32]byte{0xff}
	srv := NewServer(&fakeLookup{})
	tests := []struct {
		name    string
		spec    *rpcv1.LedgerSpecifier
		code    codes.Code
		message string
	}{
		{
			name: "malformed hash",
			spec: ledgerHashSpec([]byte("short")),
			code: codes.InvalidArgument, message: "ledgerHashMalformed",
		},
		{
			name: "missing hash",
			spec: ledgerHashSpec(missingHash[:]),
			code: codes.NotFound, message: "ledgerNotFound",
		},
		{
			name: "unknown shortcut",
			spec: ledgerShortcutSpec(rpcv1.LedgerSpecifier_Shortcut(99)),
			code: codes.InvalidArgument, message: "unknown ledger shortcut 99",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := srv.specToIndex(test.spec)
			if got != "" {
				t.Fatalf("specToIndex() = %q with error %v, want empty result", got, err)
			}
			if status.Code(err) != test.code {
				t.Fatalf("specToIndex() code = %s, want %s: %v", status.Code(err), test.code, err)
			}
			if message := status.Convert(err).Message(); message != test.message {
				t.Fatalf("specToIndex() message = %q, want %q", message, test.message)
			}
		})
	}
}

func TestGRPC_GetLedger_HeaderAndValidated(t *testing.T) {
	l := newTestLedger(t, 100, nil, nil)
	srv := NewServer(&fakeLookup{validated: l})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{
			Ledger: &rpcv1.LedgerSpecifier_Shortcut_{Shortcut: rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED},
		},
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if !resp.Validated {
		t.Errorf("expected validated=true")
	}
	if len(resp.LedgerHeader) != header.SizeWithHash {
		t.Errorf("ledger_header size=%d, want %d", len(resp.LedgerHeader), header.SizeWithHash)
	}
}

func TestGRPC_GetLedger_TransactionsHashesAndExpand(t *testing.T) {
	tx1Key := [32]byte{0xAA}
	tx2Key := [32]byte{0xBB}
	tx1, meta1 := []byte("tx-one-bytes"), []byte("meta-one-bytes")
	tx2, meta2 := []byte("tx-two-bytes"), []byte("meta-two-bytes")
	l := newTestLedger(t, 200, nil, map[[32]byte][]byte{
		tx1Key: txWithMeta(tx1, meta1),
		tx2Key: txWithMeta(tx2, meta2),
	})
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{Transactions: true})
	if err != nil {
		t.Fatalf("GetLedger hashes: %v", err)
	}
	hashes, ok := resp.Transactions.(*rpcv1.GetLedgerResponse_HashesList)
	if !ok {
		t.Fatalf("expected HashesList, got %T", resp.Transactions)
	}
	if len(hashes.HashesList.Hashes) != 2 {
		t.Errorf("expected 2 hashes, got %d", len(hashes.HashesList.Hashes))
	}

	resp, err = srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{Transactions: true, Expand: true})
	if err != nil {
		t.Fatalf("GetLedger expand: %v", err)
	}
	full, ok := resp.Transactions.(*rpcv1.GetLedgerResponse_TransactionsList)
	if !ok {
		t.Fatalf("expected TransactionsList, got %T", resp.Transactions)
	}
	if len(full.TransactionsList.Transactions) != 2 {
		t.Fatalf("expected 2 expanded txns, got %d", len(full.TransactionsList.Transactions))
	}
	// The expand path must split the stored tx+metadata blob into separate
	// transaction_blob and metadata_blob, not emit the combined payload.
	wantMeta := map[string]string{string(tx1): string(meta1), string(tx2): string(meta2)}
	for _, tm := range full.TransactionsList.Transactions {
		meta, ok := wantMeta[string(tm.TransactionBlob)]
		if !ok {
			t.Errorf("unexpected transaction_blob %q", tm.TransactionBlob)
			continue
		}
		if string(tm.MetadataBlob) != meta {
			t.Errorf("tx %q: metadata_blob=%q, want %q", tm.TransactionBlob, tm.MetadataBlob, meta)
		}
	}
}

func TestGRPC_GetLedger_MalformedExpandedTransactionReturnsPartialSuccess(t *testing.T) {
	badKey := [32]byte{0xAA}
	l := newTestLedger(t, 200, nil, map[[32]byte][]byte{badKey: pad("bad", 12)})
	srv := NewServer(&fakeLookup{openLedger: l})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{Transactions: true, Expand: true})
	if err != nil {
		t.Fatalf("GetLedger expand: %v", err)
	}
	full, ok := resp.Transactions.(*rpcv1.GetLedgerResponse_TransactionsList)
	if !ok {
		t.Fatalf("expected TransactionsList, got %T", resp.Transactions)
	}
	if len(full.TransactionsList.Transactions) != 0 {
		t.Fatalf("expanded transactions = %d, want partial empty response", len(full.TransactionsList.Transactions))
	}
}

func TestGRPC_GetLedger_LookupBySequenceAndHash(t *testing.T) {
	l := newTestLedger(t, 42, nil, nil)
	lookup := &fakeLookup{
		bySeq:  map[uint32]*ledger.Ledger{42: l},
		byHash: map[[32]byte]*ledger.Ledger{l.Hash(): l},
	}
	srv := NewServer(lookup)

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 42}},
	})
	if err != nil {
		t.Fatalf("by sequence: %v", err)
	}
	if !resp.Validated {
		t.Error("expected validated")
	}

	h := l.Hash()
	_, err = srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Hash{Hash: h[:]}},
	})
	if err != nil {
		t.Fatalf("by hash: %v", err)
	}

	_, err = srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Hash{Hash: []byte("short")}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("short hash: expected InvalidArgument, got %v", err)
	}
}

func TestGRPC_GetLedger_NoLedgerAvailableReturnsNotFound(t *testing.T) {
	srv := NewServer(&fakeLookup{})
	// Default spec maps to the open/current ledger; none is available so the
	// server must surface NotFound rather than an empty response.
	_, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGRPC_GetLedgerEntry_KeyValidation(t *testing.T) {
	l := newTestLedger(t, 7, nil, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{Key: []byte("short")})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "index malformed" {
		t.Errorf("short key: expected InvalidArgument index malformed, got %v", err)
	}
}

func TestGRPC_GetLedgerEntry_NotFound(t *testing.T) {
	l := newTestLedger(t, 7, nil, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	key := make([]byte, 32)
	key[0] = 0xCC
	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{Key: key})
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != "object not found" {
		t.Errorf("expected NotFound object not found, got %v", err)
	}
}

func TestGRPC_GetLedgerEntry_ResolvesLedgerBeforeKeyValidation(t *testing.T) {
	srv := NewServer(&fakeLookup{})
	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{Key: []byte("short")})
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != "notSynced" {
		t.Fatalf("GetLedgerEntry() error = %v, want NotFound notSynced", err)
	}
}

// TestGRPC_GetLedgerEntry_LedgerNotFound checks that a ledger-resolution
// failure (unknown sequence) maps to NotFound — not Internal.
func TestGRPC_GetLedgerEntry_LedgerNotFound(t *testing.T) {
	l := newTestLedger(t, 7, nil, nil)
	srv := NewServer(&fakeLookup{validated: l, entryErr: svcerr.ErrLedgerNotFound})

	key := make([]byte, 32)
	key[0] = 0xCC
	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{
		Key:    key,
		Ledger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 999}},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("unknown ledger sequence: expected NotFound, got %v", err)
	}
}

func TestGRPC_GetLedgerData_PaginatesAndStopsAtEndMarker(t *testing.T) {
	state := map[[32]byte][]byte{}
	for i := 1; i <= 10; i++ {
		k := [32]byte{byte(i)}
		state[k] = pad(string([]byte{byte(i)}), 12)
	}
	l := newTestLedger(t, 1, state, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	resp, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{})
	if err != nil {
		t.Fatalf("GetLedgerData: %v", err)
	}
	if len(resp.LedgerObjects.Objects) != 10 {
		t.Errorf("expected 10 objects, got %d", len(resp.LedgerObjects.Objects))
	}
	if resp.LedgerIndex != 1 {
		t.Errorf("ledger_index=%d, want 1", resp.LedgerIndex)
	}
	if len(resp.LedgerHash) != 32 {
		t.Errorf("ledger_hash len=%d, want 32", len(resp.LedgerHash))
	}
}

// TestGRPC_GetLedgerData_EndMarkerInclusive pins rippled's doLedgerDataGrpc:
// end_marker is INCLUSIVE — the scan runs up to upper_bound(end_marker), so the
// entry whose key equals end_marker is returned, not dropped.
func TestGRPC_GetLedgerData_EndMarkerInclusive(t *testing.T) {
	state := map[[32]byte][]byte{
		{0x01}: pad("a", 12),
		{0x05}: pad("b", 12),
		{0x0A}: pad("c", 12),
	}
	l := newTestLedger(t, 1, state, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	end := make([]byte, 32)
	end[0] = 0x05
	resp, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{EndMarker: end})
	if err != nil {
		t.Fatalf("GetLedgerData: %v", err)
	}
	if got := len(resp.LedgerObjects.Objects); got != 2 {
		t.Fatalf("expected 2 objs up to and including end marker, got %d", got)
	}
	last := resp.LedgerObjects.Objects[len(resp.LedgerObjects.Objects)-1].Key
	if !bytes.Equal(last, end) {
		t.Errorf("entry whose key equals end_marker must be included: last key %x, want %x", last, end)
	}
	if len(resp.Marker) != 0 {
		t.Errorf("page is not full → no resume marker, got %x", resp.Marker)
	}
}

// TestGRPC_GetLedgerData_PageFullMarkerIsFirstUnemittedMinusOne pins the
// page-full resume marker to the first un-emitted key minus one (rippled's
// `--k`), NOT the last emitted key. Keys are spaced by two so the two values
// are distinct and the off-by-one is observable.
func TestGRPC_GetLedgerData_PageFullMarkerIsFirstUnemittedMinusOne(t *testing.T) {
	const pageLimit = 2048
	state := map[[32]byte][]byte{}
	for i := 1; i <= pageLimit+1; i++ {
		state[keyFromUint(uint64(2*i))] = pad("x", 12)
	}
	l := newTestLedger(t, 1, state, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	resp, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{})
	if err != nil {
		t.Fatalf("GetLedgerData: %v", err)
	}
	if got := len(resp.LedgerObjects.Objects); got != pageLimit {
		t.Fatalf("expected a full page of %d objects, got %d", pageLimit, got)
	}

	lastEmitted := keyFromUint(uint64(2 * pageLimit))          // 4096
	firstUnemitted := keyFromUint(uint64(2 * (pageLimit + 1))) // 4098
	want := firstUnemitted
	want[31]-- // firstUnemitted - 1 (no borrow: low byte is non-zero)

	if !bytes.Equal(resp.Marker, want[:]) {
		t.Errorf("marker = %x, want first-un-emitted-minus-one %x", resp.Marker, want[:])
	}
	if bytes.Equal(resp.Marker, lastEmitted[:]) {
		t.Errorf("marker must not be the last emitted key %x (rippled off-by-one)", lastEmitted[:])
	}
}

// keyFromUint encodes v big-endian into the low 8 bytes of a 32-byte key,
// so the SHAMap orders keys by v.
func keyFromUint(v uint64) [32]byte {
	var k [32]byte
	binary.BigEndian.PutUint64(k[24:], v)
	return k
}

// TestGRPC_GetLedgerData_SyntheticMarkerResumes pins the upper_bound resume:
// a marker that is not itself a state entry (rippled returns nextKey-1 as a
// page marker) must resume at the next greater key, not truncate to empty.
func TestGRPC_GetLedgerData_SyntheticMarkerResumes(t *testing.T) {
	state := map[[32]byte][]byte{
		{0x02}: pad("a", 12),
		{0x04}: pad("b", 12),
		{0x06}: pad("c", 12),
	}
	l := newTestLedger(t, 1, state, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	// 0x03 sits between entries 0x02 and 0x04 and is not itself an entry.
	marker := make([]byte, 32)
	marker[0] = 0x03
	resp, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{Marker: marker})
	if err != nil {
		t.Fatalf("GetLedgerData: %v", err)
	}
	if got := len(resp.LedgerObjects.Objects); got != 2 {
		t.Fatalf("expected 2 objects after synthetic marker, got %d", got)
	}
	if resp.LedgerObjects.Objects[0].Key[0] != 0x04 {
		t.Errorf("expected resume at 0x04, got 0x%02x", resp.LedgerObjects.Objects[0].Key[0])
	}
}

// TestGRPC_GetLedgerData_CancelledContext checks a cancelled RPC surfaces as
// Canceled rather than an Internal server error.
func TestGRPC_GetLedgerData_CancelledContext(t *testing.T) {
	l := newTestLedger(t, 1, map[[32]byte][]byte{{0x01}: pad("a", 12)}, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := srv.GetLedgerData(ctx, &rpcv1.GetLedgerDataRequest{})
	if status.Code(err) != codes.Canceled {
		t.Errorf("expected Canceled, got %v", err)
	}
}

func TestGRPC_GetLedgerDiff_DetectsCreateModifyDelete(t *testing.T) {
	keyKeep := [32]byte{0x01}
	keyChange := [32]byte{0x02}
	keyDelete := [32]byte{0x03}
	keyCreate := [32]byte{0x04}

	base := newTestLedger(t, 10, map[[32]byte][]byte{
		keyKeep:   pad("keep-AA", 12),
		keyChange: pad("change-AA", 12),
		keyDelete: pad("delete-AA", 12),
	}, nil)
	desired := newTestLedger(t, 11, map[[32]byte][]byte{
		keyKeep:   pad("keep-AA", 12),
		keyChange: pad("change-BB", 12),
		keyCreate: pad("create-DD", 12),
	}, nil)

	srv := NewServer(&fakeLookup{
		bySeq: map[uint32]*ledger.Ledger{10: base, 11: desired},
	})

	resp, err := srv.GetLedgerDiff(context.Background(), &rpcv1.GetLedgerDiffRequest{
		BaseLedger:    &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 10}},
		DesiredLedger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 11}},
		IncludeBlobs:  true,
	})
	if err != nil {
		t.Fatalf("GetLedgerDiff: %v", err)
	}

	// Wire shape: mod_type is always UNSPECIFIED; consumers infer
	// create/modify/delete from data-presence and from comparing against the
	// base ledger.
	type seen struct {
		hasData bool
		modType rpcv1.RawLedgerObject_ModificationType
	}
	got := map[[32]byte]seen{}
	for _, obj := range resp.LedgerObjects.Objects {
		var k [32]byte
		copy(k[:], obj.Key)
		got[k] = seen{hasData: len(obj.Data) > 0, modType: obj.ModType}
	}
	for k, s := range got {
		if s.modType != rpcv1.RawLedgerObject_UNSPECIFIED {
			t.Errorf("key %x: expected mod_type UNSPECIFIED to match rippled wire shape, got %v", k, s.modType)
		}
	}
	if s, ok := got[keyCreate]; !ok || !s.hasData {
		t.Errorf("expected CREATE %x with data, got %+v", keyCreate, s)
	}
	if s, ok := got[keyChange]; !ok || !s.hasData {
		t.Errorf("expected MODIFY %x with data, got %+v", keyChange, s)
	}
	if s, ok := got[keyDelete]; !ok || s.hasData {
		t.Errorf("expected DELETE %x with empty data, got %+v", keyDelete, s)
	}
	if _, ok := got[keyKeep]; ok {
		t.Errorf("unchanged key %x should not appear in diff", keyKeep)
	}
}

func TestGRPC_GetLedgerDiff_RequiresImmutableLedgers(t *testing.T) {
	baseParent := newTestLedger(t, 9, nil, nil)
	base := newTestLedger(t, 10, nil, nil)
	desired := newTestLedger(t, 11, nil, nil)
	openBase, err := ledger.NewOpen(baseParent, time.Now())
	if err != nil {
		t.Fatalf("NewOpen base: %v", err)
	}
	openDesired, err := ledger.NewOpen(desired, time.Now())
	if err != nil {
		t.Fatalf("NewOpen desired: %v", err)
	}

	tests := []struct {
		name    string
		base    *ledger.Ledger
		desired *ledger.Ledger
		message string
	}{
		{name: "base", base: openBase, desired: desired, message: "base ledger not validated"},
		{name: "desired", base: base, desired: openDesired, message: "desired ledger not validated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{
				test.base.Sequence():    test.base,
				test.desired.Sequence(): test.desired,
			}})
			_, err := srv.GetLedgerDiff(context.Background(), &rpcv1.GetLedgerDiffRequest{
				BaseLedger:    ledgerSequenceSpec(test.base.Sequence()),
				DesiredLedger: ledgerSequenceSpec(test.desired.Sequence()),
			})
			if status.Code(err) != codes.NotFound || status.Convert(err).Message() != test.message {
				t.Fatalf("GetLedgerDiff() error = %v, want NotFound %q", err, test.message)
			}
		})
	}

	closedBase := newClosedTestLedger(t, newTestLedger(t, 20, nil, nil))
	closedDesired := newClosedTestLedger(t, closedBase)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{
		closedBase.Sequence():    closedBase,
		closedDesired.Sequence(): closedDesired,
	}})
	if _, err := srv.GetLedgerDiff(context.Background(), &rpcv1.GetLedgerDiffRequest{
		BaseLedger:    ledgerSequenceSpec(closedBase.Sequence()),
		DesiredLedger: ledgerSequenceSpec(closedDesired.Sequence()),
	}); err != nil {
		t.Fatalf("GetLedgerDiff() rejected immutable unvalidated ledgers: %v", err)
	}
}

// TestGRPC_GetLedgerData_EndMarkerBeforeMarkerRejected mirrors rippled
// LedgerData.cpp:182-186: an end_marker that sorts before the marker is
// an explicit InvalidArgument, not a silent empty-page response.
func TestGRPC_GetLedgerData_EndMarkerBeforeMarkerRejected(t *testing.T) {
	l := newTestLedger(t, 1, map[[32]byte][]byte{{0x01}: pad("a", 12)}, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	marker := make([]byte, 32)
	marker[0] = 0x05
	endMarker := make([]byte, 32)
	endMarker[0] = 0x01

	_, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{
		Marker:    marker,
		EndMarker: endMarker,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument when end_marker < marker, got %v", err)
	}
}

// TestGRPC_GetLedgerData_FollowMarkerCoversEveryEntryExactlyOnce follows the
// page-full resume marker across the fixed 2048-entry gRPC page boundary and
// asserts every entry is visited exactly once in ascending order — the resume
// invariant the first-un-emitted-minus-one marker must preserve.
func TestGRPC_GetLedgerData_FollowMarkerCoversEveryEntryExactlyOnce(t *testing.T) {
	const pageLimit = 2048
	const total = pageLimit + 5
	state := map[[32]byte][]byte{}
	for i := 1; i <= total; i++ {
		state[keyFromUint(uint64(2*i))] = pad("x", 12)
	}
	l := newTestLedger(t, 1, state, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})

	var got [][]byte
	var marker []byte
	for pages := 0; ; pages++ {
		resp, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{Marker: marker})
		if err != nil {
			t.Fatalf("GetLedgerData (page %d): %v", pages, err)
		}
		if n := len(resp.LedgerObjects.Objects); n > pageLimit {
			t.Fatalf("page %d returned %d objects, exceeds limit %d", pages, n, pageLimit)
		}
		for _, o := range resp.LedgerObjects.Objects {
			got = append(got, o.Key)
		}
		if len(resp.Marker) == 0 {
			break
		}
		marker = resp.Marker
		if pages > 4 {
			t.Fatalf("follow did not terminate (pages=%d)", pages)
		}
	}

	if len(got) != total {
		t.Fatalf("followed %d objects, want %d (gaps or repeats across the page boundary)", len(got), total)
	}
	for i := 1; i < len(got); i++ {
		if bytes.Compare(got[i-1], got[i]) >= 0 {
			t.Fatalf("objects not strictly ascending at %d: %x then %x", i, got[i-1], got[i])
		}
	}
	first := keyFromUint(2)
	last := keyFromUint(uint64(2 * total))
	if !bytes.Equal(got[0], first[:]) || !bytes.Equal(got[len(got)-1], last[:]) {
		t.Errorf("bounds: first=%x last=%x, want first=%x last=%x", got[0], got[len(got)-1], first[:], last[:])
	}
}

// TestGRPC_GetLedgerData_MalformedMarkerRejected mirrors rippled's
// doLedgerDataGrpc fromVoidChecked failure: a present but wrong-length
// marker / end_marker is InvalidArgument with rippled's exact message.
func TestGRPC_GetLedgerData_MalformedMarkerRejected(t *testing.T) {
	l := newTestLedger(t, 1, map[[32]byte][]byte{{0x01}: pad("a", 12)}, nil)
	srv := NewServer(&fakeLookup{validated: l, openLedger: l})
	short := make([]byte, 31)

	_, err := srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{Marker: short})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "marker malformed" {
		t.Errorf("short marker: got %v, want InvalidArgument \"marker malformed\"", err)
	}

	_, err = srv.GetLedgerData(context.Background(), &rpcv1.GetLedgerDataRequest{EndMarker: short})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "end marker malformed" {
		t.Errorf("short end_marker: got %v, want InvalidArgument \"end marker malformed\"", err)
	}
}

// TestGRPC_GetLedgerEntry_ByHash preserves the exact hash selector.
func TestGRPC_GetLedgerEntry_ByHash(t *testing.T) {
	l := newTestLedger(t, 9, nil, nil)
	lookup := &fakeLookup{
		validated: l,
		byHash:    map[[32]byte]*ledger.Ledger{l.Hash(): l},
	}
	srv := NewServer(lookup)

	key := make([]byte, 32)
	key[0] = 0xCC
	h := l.Hash()
	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{
		Key:    key,
		Ledger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Hash{Hash: h[:]}},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("by-hash GetLedgerEntry: expected NotFound, got %v", err)
	}
	if want := hex.EncodeToString(h[:]); lookup.entryIndex != want {
		t.Errorf("ledger selector = %q, want exact hash %q", lookup.entryIndex, want)
	}
}

// TestGRPC_GetLedger_UnspecifiedShortcutResolvesToOpen pins the
// SHORTCUT_UNSPECIFIED (or absent) routing to the open/current ledger.
func TestGRPC_GetLedger_UnspecifiedShortcutResolvesToOpen(t *testing.T) {
	open := newTestLedger(t, 50, nil, nil)
	validated := newTestLedger(t, 25, nil, nil)
	srv := NewServer(&fakeLookup{openLedger: open, validated: validated})

	want := header.AddRaw(open.Header(), true)

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{
			Ledger: &rpcv1.LedgerSpecifier_Shortcut_{Shortcut: rpcv1.LedgerSpecifier_SHORTCUT_UNSPECIFIED},
		},
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if !bytes.Equal(resp.LedgerHeader, want) {
		t.Errorf("UNSPECIFIED routed to wrong ledger: header mismatch (got len %d, want %d)", len(resp.LedgerHeader), len(want))
	}
}

// TestGRPC_GetLedger_GetObjectsDiffsParent exercises the get_objects branch:
// the changed state objects between a ledger and its parent, each tagged
// CREATED / MODIFIED / DELETED.
func TestGRPC_GetLedger_GetObjectsDiffsParent(t *testing.T) {
	keyKeep := [32]byte{0x01}
	keyChange := [32]byte{0x02}
	keyDelete := [32]byte{0x03}
	keyCreate := [32]byte{0x04}

	parent := newTestLedger(t, 9, map[[32]byte][]byte{
		keyKeep:   pad("keep-AA", 12),
		keyChange: pad("change-AA", 12),
		keyDelete: pad("delete-AA", 12),
	}, nil)
	desired := newTestLedger(t, 10, map[[32]byte][]byte{
		keyKeep:   pad("keep-AA", 12),
		keyChange: pad("change-BB", 12),
		keyCreate: pad("create-DD", 12),
	}, nil)

	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{9: parent, 10: desired}})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:     &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 10}},
		GetObjects: true,
	})
	if err != nil {
		t.Fatalf("GetLedger get_objects: %v", err)
	}
	if !resp.ObjectsIncluded {
		t.Error("expected objects_included=true")
	}

	type seen struct {
		hasData bool
		modType rpcv1.RawLedgerObject_ModificationType
	}
	got := map[[32]byte]seen{}
	for _, obj := range resp.LedgerObjects.Objects {
		var k [32]byte
		copy(k[:], obj.Key)
		got[k] = seen{hasData: len(obj.Data) > 0, modType: obj.ModType}
	}
	if s, ok := got[keyCreate]; !ok || s.modType != rpcv1.RawLedgerObject_CREATED || !s.hasData {
		t.Errorf("create %x: got %+v", keyCreate, s)
	}
	if s, ok := got[keyChange]; !ok || s.modType != rpcv1.RawLedgerObject_MODIFIED || !s.hasData {
		t.Errorf("modify %x: got %+v", keyChange, s)
	}
	if s, ok := got[keyDelete]; !ok || s.modType != rpcv1.RawLedgerObject_DELETED || s.hasData {
		t.Errorf("delete %x: got %+v", keyDelete, s)
	}
	if _, ok := got[keyKeep]; ok {
		t.Errorf("unchanged key %x should not appear", keyKeep)
	}
}

// TestGRPC_GetLedger_GetObjectsParentMissing checks that an absent parent
// ledger surfaces as NotFound.
func TestGRPC_GetLedger_GetObjectsParentMissing(t *testing.T) {
	desired := newTestLedger(t, 10, map[[32]byte][]byte{{0x01}: pad("a", 12)}, nil)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{10: desired}})

	_, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:     &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 10}},
		GetObjects: true,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound when parent absent, got %v", err)
	}
}

func TestGRPC_GetLedger_GetObjectsRequiresImmutableLedgers(t *testing.T) {
	parent := newTestLedger(t, 9, nil, nil)
	openParent, err := ledger.NewOpen(newTestLedger(t, 8, nil, nil), time.Now())
	if err != nil {
		t.Fatalf("NewOpen parent: %v", err)
	}
	openDesired, err := ledger.NewOpen(parent, time.Now())
	if err != nil {
		t.Fatalf("NewOpen desired: %v", err)
	}

	tests := []struct {
		name    string
		parent  *ledger.Ledger
		desired *ledger.Ledger
		message string
	}{
		{name: "parent", parent: openParent, desired: newTestLedger(t, 10, nil, nil), message: "parent ledger not validated"},
		{name: "desired", parent: parent, desired: openDesired, message: "ledger not validated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{
				test.parent.Sequence():  test.parent,
				test.desired.Sequence(): test.desired,
			}})
			_, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
				Ledger:     ledgerSequenceSpec(test.desired.Sequence()),
				GetObjects: true,
			})
			if status.Code(err) != codes.NotFound || status.Convert(err).Message() != test.message {
				t.Fatalf("GetLedger() error = %v, want NotFound %q", err, test.message)
			}
		})
	}

	closedParent := newClosedTestLedger(t, newTestLedger(t, 20, nil, nil))
	closedDesired := newClosedTestLedger(t, closedParent)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{
		closedParent.Sequence():  closedParent,
		closedDesired.Sequence(): closedDesired,
	}})
	if _, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:     ledgerSequenceSpec(closedDesired.Sequence()),
		GetObjects: true,
	}); err != nil {
		t.Fatalf("GetLedger() rejected immutable unvalidated ledgers: %v", err)
	}
}

// TestGRPC_GetLedger_ObjectNeighbors checks the get_object_neighbors branch:
// each created/deleted object carries its predecessor and successor from the
// desired state map, modified objects carry neither, and the response flags
// object_neighbors_included.
func TestGRPC_GetLedger_ObjectNeighbors(t *testing.T) {
	k1 := [32]byte{0x01}
	k2 := [32]byte{0x02}
	k3 := [32]byte{0x03}
	k4 := [32]byte{0x04}
	k5 := [32]byte{0x05}

	parent := newTestLedger(t, 10, map[[32]byte][]byte{
		k1: pad("a", 12), k2: pad("b", 12), k4: pad("d", 12), k5: pad("e1", 12),
	}, nil)
	desired := newTestLedger(t, 11, map[[32]byte][]byte{
		k1: pad("a", 12), k3: pad("c", 12), k4: pad("d", 12), k5: pad("e2", 12),
	}, nil)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{10: parent, 11: desired}})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:             &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 11}},
		GetObjects:         true,
		GetObjectNeighbors: true,
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if !resp.ObjectNeighborsIncluded {
		t.Error("object_neighbors_included must be true when requested")
	}

	got := map[[32]byte]*rpcv1.RawLedgerObject{}
	for _, o := range resp.LedgerObjects.Objects {
		var k [32]byte
		copy(k[:], o.Key)
		got[k] = o
	}

	// Created 0x03: neighbours in the desired map are 0x01 and 0x04.
	if o := got[k3]; o == nil || o.ModType != rpcv1.RawLedgerObject_CREATED {
		t.Fatalf("expected CREATED 0x03, got %+v", o)
	} else {
		if !bytes.Equal(o.Predecessor, k1[:]) {
			t.Errorf("created predecessor = %x, want %x", o.Predecessor, k1)
		}
		if !bytes.Equal(o.Successor, k4[:]) {
			t.Errorf("created successor = %x, want %x", o.Successor, k4)
		}
	}
	// Deleted 0x02: neighbours come from the desired map (0x01, 0x03).
	if o := got[k2]; o == nil || o.ModType != rpcv1.RawLedgerObject_DELETED {
		t.Fatalf("expected DELETED 0x02, got %+v", o)
	} else {
		if !bytes.Equal(o.Predecessor, k1[:]) {
			t.Errorf("deleted predecessor = %x, want %x", o.Predecessor, k1)
		}
		if !bytes.Equal(o.Successor, k3[:]) {
			t.Errorf("deleted successor = %x, want %x", o.Successor, k3)
		}
	}
	// Modified 0x05: no neighbours.
	if o := got[k5]; o == nil || o.ModType != rpcv1.RawLedgerObject_MODIFIED {
		t.Fatalf("expected MODIFIED 0x05, got %+v", o)
	} else if o.Predecessor != nil || o.Successor != nil {
		t.Errorf("modified object must not carry neighbours, got pred=%x succ=%x", o.Predecessor, o.Successor)
	}
}

// TestGRPC_GetLedger_ObjectNeighborsOmitted confirms neighbours stay empty and
// object_neighbors_included stays false when the caller does not request them.
func TestGRPC_GetLedger_ObjectNeighborsOmitted(t *testing.T) {
	parent := newTestLedger(t, 10, map[[32]byte][]byte{{0x01}: pad("a", 12)}, nil)
	desired := newTestLedger(t, 11, map[[32]byte][]byte{{0x01}: pad("a", 12), {0x02}: pad("b", 12)}, nil)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{10: parent, 11: desired}})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:     &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 11}},
		GetObjects: true,
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if resp.ObjectNeighborsIncluded {
		t.Error("object_neighbors_included must be false when not requested")
	}
	for _, o := range resp.LedgerObjects.Objects {
		if o.Predecessor != nil || o.Successor != nil {
			t.Errorf("object %x carried neighbours unrequested", o.Key)
		}
	}
}

// TestGRPC_GetLedger_BookSuccessor checks that creating the first directory
// page of an order book emits a book successor keyed by the book base, while a
// created owner directory does not.
func TestGRPC_GetLedger_BookSuccessor(t *testing.T) {
	bookKey := [32]byte{0: 0x20, 31: 0x07}
	ownerKey := [32]byte{0: 0x30, 31: 0x07}
	wantBase := keylet.Quality(keylet.Keylet{Type: 0x0064, Key: bookKey}, 0).Key

	parent := newTestLedger(t, 10, map[[32]byte][]byte{}, nil)
	desired := newTestLedger(t, 11, map[[32]byte][]byte{
		bookKey:  encodeBookDir(t),
		ownerKey: encodeOwnerDir(t),
	}, nil)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{10: parent, 11: desired}})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:             &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 11}},
		GetObjects:         true,
		GetObjectNeighbors: true,
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if len(resp.BookSuccessors) != 1 {
		t.Fatalf("expected exactly one book successor (book dir only), got %d", len(resp.BookSuccessors))
	}
	bs := resp.BookSuccessors[0]
	if !bytes.Equal(bs.BookBase, wantBase[:]) {
		t.Errorf("book_base = %x, want %x", bs.BookBase, wantBase)
	}
	if !bytes.Equal(bs.FirstBook, bookKey[:]) {
		t.Errorf("first_book = %x, want %x", bs.FirstBook, bookKey)
	}
}

// TestGRPC_GetLedger_BookSuccessorOnDelete checks that removing the only page
// of an order book emits a book successor keyed by the book base with an empty
// first_book (no book remains in the desired ledger).
func TestGRPC_GetLedger_BookSuccessorOnDelete(t *testing.T) {
	bookKey := [32]byte{0: 0x20, 31: 0x07}
	wantBase := keylet.Quality(keylet.Keylet{Type: 0x0064, Key: bookKey}, 0).Key

	parent := newTestLedger(t, 10, map[[32]byte][]byte{bookKey: encodeBookDir(t)}, nil)
	desired := newTestLedger(t, 11, map[[32]byte][]byte{}, nil)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{10: parent, 11: desired}})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:             &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 11}},
		GetObjects:         true,
		GetObjectNeighbors: true,
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if len(resp.BookSuccessors) != 1 {
		t.Fatalf("expected one book successor for the removed book, got %d", len(resp.BookSuccessors))
	}
	bs := resp.BookSuccessors[0]
	if !bytes.Equal(bs.BookBase, wantBase[:]) {
		t.Errorf("book_base = %x, want %x", bs.BookBase, wantBase)
	}
	if len(bs.FirstBook) != 0 {
		t.Errorf("first_book must be empty when no book remains, got %x", bs.FirstBook)
	}
}

// TestGRPC_GetLedger_BookSuccessorDeleteKeepsNext deletes the best-quality
// page of an order book that still has a worse-quality page; the book
// successor's first_book must point at the surviving page.
func TestGRPC_GetLedger_BookSuccessorDeleteKeepsNext(t *testing.T) {
	best := [32]byte{0: 0x20, 31: 0x03}  // lower quality bits sort first
	worse := [32]byte{0: 0x20, 31: 0x09} // same book base, survives
	wantBase := keylet.Quality(keylet.Keylet{Type: 0x0064, Key: best}, 0).Key

	parent := newTestLedger(t, 10, map[[32]byte][]byte{
		best:  encodeBookDir(t),
		worse: encodeBookDir(t),
	}, nil)
	desired := newTestLedger(t, 11, map[[32]byte][]byte{
		worse: encodeBookDir(t),
	}, nil)
	srv := NewServer(&fakeLookup{bySeq: map[uint32]*ledger.Ledger{10: parent, 11: desired}})

	resp, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
		Ledger:             &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Sequence{Sequence: 11}},
		GetObjects:         true,
		GetObjectNeighbors: true,
	})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if len(resp.BookSuccessors) != 1 {
		t.Fatalf("expected one book successor for the removed head, got %d", len(resp.BookSuccessors))
	}
	bs := resp.BookSuccessors[0]
	if !bytes.Equal(bs.BookBase, wantBase[:]) {
		t.Errorf("book_base = %x, want %x", bs.BookBase, wantBase)
	}
	if !bytes.Equal(bs.FirstBook, worse[:]) {
		t.Errorf("first_book = %x, want surviving page %x", bs.FirstBook, worse)
	}
}

func TestIsBookDirectory(t *testing.T) {
	if !isBookDirectory(encodeBookDir(t)) {
		t.Error("directory without Owner must be a book directory")
	}
	if isBookDirectory(encodeOwnerDir(t)) {
		t.Error("directory with Owner is not a book directory")
	}
	if isBookDirectory(pad("not-a-dir", 12)) {
		t.Error("non-directory blob must not be a book directory")
	}
	if isBookDirectory([]byte{0x11, 0x00}) {
		t.Error("too-short blob must not be a book directory")
	}
}

func TestGetQualityNext(t *testing.T) {
	base := [32]byte{0: 0x20}
	want := [32]byte{0: 0x20}
	want[23] = 0x01 // +2^64 lands on byte 23
	if got := getQualityNext(base); got != want {
		t.Errorf("getQualityNext: got %x, want %x", got, want)
	}
}

func encodeBookDir(t *testing.T) []byte {
	t.Helper()
	b, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "DirectoryNode",
		"Flags":             uint32(0),
		"RootIndex":         "0000000000000000000000000000000000000000000000000000000000000000",
		"TakerPaysCurrency": "0000000000000000000000000000000000000000",
		"TakerPaysIssuer":   "0000000000000000000000000000000000000000",
		"TakerGetsCurrency": "0000000000000000000000000000000000000000",
		"TakerGetsIssuer":   "0000000000000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("encode book directory: %v", err)
	}
	return b
}

func encodeOwnerDir(t *testing.T) []byte {
	t.Helper()
	owner, err := addresscodec.EncodeAccountIDToClassicAddress(make([]byte, 20))
	if err != nil {
		t.Fatalf("encode owner address: %v", err)
	}
	b, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Flags":           uint32(0),
		"RootIndex":       "0000000000000000000000000000000000000000000000000000000000000000",
		"Owner":           owner,
	})
	if err != nil {
		t.Fatalf("encode owner directory: %v", err)
	}
	return b
}

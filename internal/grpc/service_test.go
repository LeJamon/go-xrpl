package grpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
}

func (f *fakeLookup) GetLedgerByHash(h [32]byte) (*ledger.Ledger, error) {
	if l, ok := f.byHash[h]; ok {
		return l, nil
	}
	return nil, errors.New("not found")
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
func (f *fakeLookup) GetLedgerEntry(_ context.Context, key [32]byte, _ string) (*service.LedgerEntryResult, error) {
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
	hdr.Hash = [32]byte{byte(seq), 0xAB}
	return ledger.FromGenesis(hdr, stateMap, txMap, drops.Fees{})
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
	l := newTestLedger(t, 200, nil, map[[32]byte][]byte{
		tx1Key: pad("tx1blob", 12),
		tx2Key: pad("tx2blob", 12),
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
		t.Errorf("expected 2 expanded txns, got %d", len(full.TransactionsList.Transactions))
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
	// Default spec maps to the open/current ledger (rippled
	// RPCHelpers.cpp:456-471); none is available so the server must
	// surface NotFound rather than an empty response.
	_, err := srv.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGRPC_GetLedgerEntry_KeyValidation(t *testing.T) {
	l := newTestLedger(t, 7, nil, nil)
	srv := NewServer(&fakeLookup{validated: l})

	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{Key: []byte("short")})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("short key: expected InvalidArgument, got %v", err)
	}
}

func TestGRPC_GetLedgerEntry_NotFound(t *testing.T) {
	l := newTestLedger(t, 7, nil, nil)
	srv := NewServer(&fakeLookup{validated: l})

	key := make([]byte, 32)
	key[0] = 0xCC
	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{Key: key})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
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

// TestGRPC_GetLedgerData_EndMarkerInclusive mirrors rippled's
// doLedgerDataGrpc (LedgerData.cpp): end_marker is INCLUSIVE — the loop
// runs up to upper_bound(end_marker), so the entry whose key equals
// end_marker is returned, not dropped.
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

// TestGRPC_GetLedgerData_PageFullMarkerIsFirstUnemittedMinusOne mirrors
// rippled's doLedgerDataGrpc page-full path (`--k`): the resume marker is
// the first un-emitted key minus one, NOT the last emitted key. Keys are
// spaced by two so the two values are distinct and the off-by-one is
// observable.
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

	// Wire-shape matches rippled LedgerDiff.cpp:63-85: mod_type is
	// always UNSPECIFIED; consumers infer create/modify/delete from
	// data-presence and from comparing against the base ledger.
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

// TestGRPC_GetLedgerEntry_ByHash exercises a hash-based LedgerSpecifier
// being resolved through LedgerLookup.GetLedgerByHash and flattened into
// the sequence path, matching rippled RPCHelpers.cpp:415-450.
func TestGRPC_GetLedgerEntry_ByHash(t *testing.T) {
	l := newTestLedger(t, 9, nil, nil)
	srv := NewServer(&fakeLookup{
		validated: l,
		byHash:    map[[32]byte]*ledger.Ledger{l.Hash(): l},
	})

	key := make([]byte, 32)
	key[0] = 0xCC
	h := l.Hash()
	_, err := srv.GetLedgerEntry(context.Background(), &rpcv1.GetLedgerEntryRequest{
		Key:    key,
		Ledger: &rpcv1.LedgerSpecifier{Ledger: &rpcv1.LedgerSpecifier_Hash{Hash: h[:]}},
	})
	// The fake ledger has no state at this key, so NotFound is the
	// expected resolution of a successful hash → sequence resolution.
	if status.Code(err) != codes.NotFound {
		t.Errorf("by-hash GetLedgerEntry: expected NotFound, got %v", err)
	}
}

// TestGRPC_GetLedger_UnspecifiedShortcutResolvesToOpen pins the
// SHORTCUT_UNSPECIFIED (or absent) routing to the open/current ledger,
// matching rippled RPCHelpers.cpp:456-471.
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
	if len(resp.LedgerHeader) != len(want) || !bytesEqual(resp.LedgerHeader, want) {
		t.Errorf("UNSPECIFIED routed to wrong ledger: header mismatch (got len %d, want %d)", len(resp.LedgerHeader), len(want))
	}
}

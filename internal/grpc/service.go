// Package grpc implements the XRPLedgerAPIService gRPC surface mirroring
// rippled's binary-only ledger RPCs (the API surface consumed by Clio):
// GetLedger, GetLedgerEntry, GetLedgerData and GetLedgerDiff. Ledger lookups
// are delegated to the existing internal/ledger/service.Service so the gRPC
// and JSON-RPC surfaces stay behaviourally consistent.
package grpc

import (
	"bytes"
	"context"
	"errors"
	"math"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

const maxValidatedLedgerAge = 120 * time.Second

var errLedgerNotSynced = errors.New("notSynced")

// LedgerLookup is the slice of the ledger Service that this gRPC
// implementation needs. Kept narrow so tests can substitute a fake.
type LedgerLookup interface {
	GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error)
	GetLedgerBySequence(seq uint32) (*ledger.Ledger, error)
	GetClosedLedger() *ledger.Ledger
	GetValidatedLedger() *ledger.Ledger
	GetValidatedLedgerAge() time.Duration
	GetOpenLedger() *ledger.Ledger
	IsStandalone() bool
	GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*service.LedgerEntryResult, error)
}

type Server struct {
	rpcv1.UnimplementedXRPLedgerAPIServiceServer
	lookup LedgerLookup
}

func NewServer(lookup LedgerLookup) *Server {
	return &Server{lookup: lookup}
}

func (s *Server) resolveLedger(spec *rpcv1.LedgerSpecifier) (*ledger.Ledger, error) {
	selection, err := selectorFromSpecifier(spec)
	if err != nil {
		return nil, err
	}
	result, err := s.resolveLedgerSelection(selection)
	return result.Value, err
}

func selectorFromSpecifier(spec *rpcv1.LedgerSpecifier) (ledgerselector.Selector, error) {
	if spec == nil || spec.Ledger == nil {
		return ledgerselector.Absent(), nil
	}

	switch value := spec.Ledger.(type) {
	case *rpcv1.LedgerSpecifier_Shortcut_:
		switch value.Shortcut {
		case rpcv1.LedgerSpecifier_SHORTCUT_UNSPECIFIED, rpcv1.LedgerSpecifier_SHORTCUT_CURRENT:
			return ledgerselector.Current(), nil
		case rpcv1.LedgerSpecifier_SHORTCUT_CLOSED:
			return ledgerselector.Closed(), nil
		case rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED:
			return ledgerselector.Validated(), nil
		default:
			return ledgerselector.Selector{}, status.Errorf(codes.InvalidArgument, "unknown ledger shortcut %v", value.Shortcut)
		}
	case *rpcv1.LedgerSpecifier_Sequence:
		return ledgerselector.FromSequence(value.Sequence), nil
	case *rpcv1.LedgerSpecifier_Hash:
		if len(value.Hash) != 32 {
			return ledgerselector.Selector{}, status.Error(codes.InvalidArgument, "ledgerHashMalformed")
		}
		var hash [32]byte
		copy(hash[:], value.Hash)
		return ledgerselector.FromHash(hash), nil
	default:
		return ledgerselector.Selector{}, status.Error(codes.InvalidArgument, "ledger specifier malformed")
	}
}

func (s *Server) resolveLedgerSelection(selection ledgerselector.Selector) (ledgerselector.Result[*ledger.Ledger], error) {
	current := func() (*ledger.Ledger, bool, error) {
		if stale, _ := s.validatedLedgerState(); stale {
			return nil, false, errLedgerNotSynced
		}
		l := s.lookup.GetOpenLedger()
		if s.shortcutLedgerLagged(l) {
			return nil, false, errLedgerNotSynced
		}
		return l, l != nil, nil
	}
	result, err := ledgerselector.Resolve(selection, ledgerselector.Callbacks[*ledger.Ledger]{
		Absent:  current,
		Current: current,
		Closed: func() (*ledger.Ledger, bool, error) {
			if stale, _ := s.validatedLedgerState(); stale {
				return nil, false, errLedgerNotSynced
			}
			l := s.lookup.GetClosedLedger()
			if s.shortcutLedgerLagged(l) {
				return nil, false, errLedgerNotSynced
			}
			return l, l != nil, nil
		},
		Validated: func() (*ledger.Ledger, bool, error) {
			if stale, _ := s.validatedLedgerState(); stale {
				return nil, false, errLedgerNotSynced
			}
			l := s.lookup.GetValidatedLedger()
			return l, l != nil, nil
		},
		BySequence: func(sequence uint32) (*ledger.Ledger, bool, error) {
			l, err := s.lookup.GetLedgerBySequence(sequence)
			if l != nil {
				if stale, validSequence := s.validatedLedgerState(); stale && l.Sequence() > validSequence {
					return nil, false, errLedgerNotSynced
				}
			}
			return l, l != nil, err
		},
		ByHash: func(hash [32]byte) (*ledger.Ledger, bool, error) {
			l, err := s.lookup.GetLedgerByHash(hash)
			return l, l != nil, err
		},
	})
	if err != nil {
		return ledgerselector.Result[*ledger.Ledger]{}, ledgerSelectorError(selection, err)
	}
	return result, nil
}

func (s *Server) validatedLedgerState() (stale bool, sequence uint32) {
	if s.lookup.IsStandalone() {
		return false, 0
	}
	validated := s.lookup.GetValidatedLedger()
	if validated == nil {
		return true, 0
	}
	return s.lookup.GetValidatedLedgerAge() > maxValidatedLedgerAge, validated.Sequence()
}

func (s *Server) shortcutLedgerLagged(l *ledger.Ledger) bool {
	if l == nil || s.lookup.IsStandalone() {
		return false
	}
	validated := s.lookup.GetValidatedLedger()
	return validated != nil && uint64(l.Sequence())+10 < uint64(validated.Sequence())
}

func ledgerSelectorError(selection ledgerselector.Selector, err error) error {
	if errors.Is(err, ledgerselector.ErrInvalidSelector) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if errors.Is(err, errLedgerNotSynced) {
		return status.Error(codes.NotFound, errLedgerNotSynced.Error())
	}

	switch selection.Kind() {
	case ledgerselector.KindAbsent, ledgerselector.KindCurrent:
		return status.Error(codes.NotFound, "notSynced")
	case ledgerselector.KindClosed:
		return status.Error(codes.NotFound, "notSynced")
	case ledgerselector.KindValidated:
		return status.Error(codes.NotFound, "notSynced")
	case ledgerselector.KindSequence:
		return status.Error(codes.NotFound, "ledgerNotFound")
	case ledgerselector.KindHash:
		return status.Error(codes.NotFound, "ledgerNotFound")
	default:
		return status.Error(codes.InvalidArgument, ledgerselector.ErrInvalidSelector.Error())
	}
}

// GetLedger returns a ledger header and, on request, its transaction set
// (hashes or expanded blobs) and the objects that changed versus its parent.
func (s *Server) GetLedger(ctx context.Context, req *rpcv1.GetLedgerRequest) (*rpcv1.GetLedgerResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	l, err := s.resolveLedger(req.GetLedger())
	if err != nil {
		return nil, err
	}

	resp := &rpcv1.GetLedgerResponse{
		LedgerHeader: header.AddRaw(l.Header(), true),
		Validated:    l.IsValidated(),
	}

	if req.GetTransactions() {
		if req.GetExpand() {
			list, err := expandTransactions(l)
			if err != nil {
				return nil, err
			}
			resp.Transactions = &rpcv1.GetLedgerResponse_TransactionsList{TransactionsList: list}
		} else {
			hashes := &rpcv1.TransactionHashList{}
			if err := l.ForEachTransaction(func(h [32]byte, _ []byte) bool {
				hashes.Hashes = append(hashes.Hashes, cloneHash(h))
				return true
			}); err != nil {
				return nil, status.Errorf(codes.Internal, "iterating transactions: %v", err)
			}
			resp.Transactions = &rpcv1.GetLedgerResponse_HashesList{HashesList: hashes}
		}
	}

	if req.GetGetObjects() {
		if err := s.appendChangedObjects(resp, l, req.GetGetObjectNeighbors()); err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// expandTransactions splits each stored tx+metadata blob into its separate
// transaction and metadata serializations, the shape Clio expects.
func expandTransactions(l *ledger.Ledger) (*rpcv1.TransactionAndMetadataList, error) {
	list := &rpcv1.TransactionAndMetadataList{}
	var splitErr error
	if err := l.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		txBlob, metaBlob, e := tx.SplitTxWithMetaBlob(data)
		if e != nil {
			splitErr = e
			return false
		}
		list.Transactions = append(list.Transactions, &rpcv1.TransactionAndMetadata{
			TransactionBlob: append([]byte(nil), txBlob...),
			MetadataBlob:    append([]byte(nil), metaBlob...),
		})
		return true
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "iterating transactions: %v", err)
	}
	if splitErr != nil {
		return nil, status.Errorf(codes.Internal, "splitting transaction blob: %v", splitErr)
	}
	return list, nil
}

// appendChangedObjects fills the response with the state objects that differ
// between l and its parent (sequence-1), tagging each CREATED, MODIFIED or
// DELETED. When wantNeighbors is set it also fills each created/deleted
// object's predecessor and successor and any order-book successors.
func (s *Server) appendChangedObjects(resp *rpcv1.GetLedgerResponse, l *ledger.Ledger, wantNeighbors bool) error {
	parent, err := s.lookup.GetLedgerBySequence(l.Sequence() - 1)
	if err != nil {
		return status.Error(codes.NotFound, "parent ledger not validated")
	}
	diff, baseMap, desiredMap, err := stateDiff(parent, l)
	if err != nil {
		return status.Errorf(codes.Internal, "comparing state maps: %v", err)
	}
	if !diff.Complete {
		return status.Error(codes.ResourceExhausted, "too many differences between specified ledgers")
	}
	objects := &rpcv1.RawLedgerObjects{}
	for _, d := range diff.Differences {
		obj := &rpcv1.RawLedgerObject{Key: cloneHash(d.Key)}
		switch d.Type {
		case shamap.DiffAdded:
			obj.ModType = rpcv1.RawLedgerObject_CREATED
			obj.Data = d.SecondItem.Data()
		case shamap.DiffModified:
			obj.ModType = rpcv1.RawLedgerObject_MODIFIED
			obj.Data = d.SecondItem.Data()
		case shamap.DiffRemoved:
			obj.ModType = rpcv1.RawLedgerObject_DELETED
		}
		// Neighbours are computed only for created and deleted objects, not
		// modified ones.
		if wantNeighbors && d.Type != shamap.DiffModified {
			appendNeighbors(obj, resp, d, baseMap, desiredMap)
		}
		objects.Objects = append(objects.Objects, obj)
	}
	resp.LedgerObjects = objects
	resp.ObjectsIncluded = true
	resp.ObjectNeighborsIncluded = wantNeighbors
	resp.SkiplistIncluded = true
	return nil
}

// appendNeighbors fills the predecessor and successor of a created or deleted
// object and, for the first page of an order book, the book successor.
// Predecessor and successor come from the desired (new) state map; the book
// successor is keyed by the deleted book in the base map or the created book
// in the desired map.
func appendNeighbors(obj *rpcv1.RawLedgerObject, resp *rpcv1.GetLedgerResponse, d shamap.DifferenceItem, baseMap, desiredMap *shamap.SHAMap) {
	k := d.Key
	if it := desiredMap.LowerBound(k); it.Valid() {
		obj.Predecessor = cloneHash(it.Item().Key())
	}
	if it := desiredMap.UpperBound(k); it.Valid() {
		obj.Successor = cloneHash(it.Item().Key())
	}

	var blob []byte
	switch d.Type {
	case shamap.DiffAdded:
		blob = d.SecondItem.DataUnsafe()
	case shamap.DiffRemoved:
		blob = d.FirstItem.DataUnsafe()
	}
	if !isBookDirectory(blob) {
		return
	}

	bookBase := keylet.Quality(keylet.Keylet{Type: entry.TypeDirectoryNode, Key: k}, 0).Key
	bookEnd := getQualityNext(bookBase)
	inBook := func(key [32]byte) bool { return bytes.Compare(key[:], bookEnd[:]) < 0 }

	switch d.Type {
	case shamap.DiffAdded:
		if it := desiredMap.UpperBound(bookBase); it.Valid() {
			if first := it.Item().Key(); inBook(first) && first == k {
				resp.BookSuccessors = append(resp.BookSuccessors, &rpcv1.BookSuccessor{
					BookBase:  cloneHash(bookBase),
					FirstBook: cloneHash(first),
				})
			}
		}
	case shamap.DiffRemoved:
		if it := baseMap.UpperBound(bookBase); it.Valid() {
			if old := it.Item().Key(); inBook(old) && old == k {
				succ := &rpcv1.BookSuccessor{BookBase: cloneHash(bookBase)}
				if it2 := desiredMap.UpperBound(bookBase); it2.Valid() {
					if next := it2.Item().Key(); inBook(next) {
						succ.FirstBook = cloneHash(next)
					}
				}
				resp.BookSuccessors = append(resp.BookSuccessors, succ)
			}
		}
	}
}

// isBookDirectory reports whether blob is a serialized directory node that
// roots an order book — a directory without an Owner field (owner directories
// carry sfOwner; book directories do not).
func isBookDirectory(blob []byte) bool {
	if len(blob) < 3 {
		return false
	}
	if entry.Type(uint16(blob[1])<<8|uint16(blob[2])) != entry.TypeDirectoryNode {
		return false
	}
	fields, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		return false
	}
	_, hasOwner := fields["Owner"]
	return !hasOwner
}

// getQualityNext returns base + 2^64, the smallest key past the highest
// quality of the order book rooted at base. The quality occupies the low 64
// bits, so the increment lands on byte 23.
func getQualityNext(base [32]byte) [32]byte {
	for i := 23; i >= 0; i-- {
		base[i]++
		if base[i] != 0 {
			break
		}
	}
	return base
}

// GetLedgerEntry returns the raw bytes of a single ledger entry.
func (s *Server) GetLedgerEntry(ctx context.Context, req *rpcv1.GetLedgerEntryRequest) (*rpcv1.GetLedgerEntryResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	key, err := hash32(req.GetKey(), "entry key")
	if err != nil {
		return nil, err
	}

	ledgerIdx, err := s.specToIndex(req.GetLedger())
	if err != nil {
		return nil, err
	}

	entry, err := s.lookup.GetLedgerEntry(ctx, key, ledgerIdx)
	if err != nil {
		switch {
		case errors.Is(err, svcerr.ErrLedgerEntryNotFound):
			return nil, status.Error(codes.NotFound, "ledger entry not found")
		case errors.Is(err, svcerr.ErrLedgerNotFound), errors.Is(err, svcerr.ErrNoOpenLedger):
			return nil, status.Error(codes.NotFound, err.Error())
		default:
			return nil, status.Errorf(codes.Internal, "lookup: %v", err)
		}
	}

	return &rpcv1.GetLedgerEntryResponse{
		LedgerObject: &rpcv1.RawLedgerObject{
			Data: entry.Node,
			Key:  cloneHash(key),
		},
		Ledger: req.GetLedger(),
	}, nil
}

// GetLedgerData iterates a ledger's state entries, paginated by marker /
// end_marker. The page cap is the binary pageLength rippled uses for the gRPC
// surface; resume is strictly after marker and end_marker is inclusive.
func (s *Server) GetLedgerData(ctx context.Context, req *rpcv1.GetLedgerDataRequest) (*rpcv1.GetLedgerDataResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	l, err := s.resolveLedger(req.GetLedger())
	if err != nil {
		return nil, err
	}

	var startKey [32]byte
	hasMarker := false
	if m := req.GetMarker(); len(m) > 0 {
		if startKey, err = hash32(m, "marker"); err != nil {
			return nil, status.Error(codes.InvalidArgument, "marker malformed")
		}
		hasMarker = true
	}

	var endKey [32]byte
	hasEnd := false
	if m := req.GetEndMarker(); len(m) > 0 {
		if endKey, err = hash32(m, "end_marker"); err != nil {
			return nil, status.Error(codes.InvalidArgument, "end marker malformed")
		}
		hasEnd = true
	}
	if hasMarker && hasEnd && compareKey(endKey, startKey) < 0 {
		return nil, status.Error(codes.InvalidArgument, "end marker out of range")
	}

	const pageLimit = 2048
	resp := &rpcv1.GetLedgerDataResponse{
		LedgerIndex:   l.Sequence(),
		LedgerHash:    cloneHash(l.Hash()),
		LedgerObjects: &rpcv1.RawLedgerObjects{},
	}

	// Resume strictly after the marker via the shared state iterator; the zero
	// startKey starts from the first entry. A since-deleted marker continues
	// from the next entry rather than rescanning or returning an empty page.
	count := 0
	if err := l.IterateStateFrom(ctx, startKey, func(key [32]byte, data []byte) bool {
		// end_marker is inclusive: stop only past it, so an entry whose key
		// equals end_marker is still returned.
		if hasEnd && compareKey(key, endKey) > 0 {
			return false
		}
		if count >= pageLimit {
			// One entry past the page. Resume is strictly-greater than the
			// marker, so record the first un-emitted key minus one — the next
			// page then begins exactly at that entry.
			resp.Marker = cloneHash(ledger.DecrementKey(key))
			return false
		}
		resp.LedgerObjects.Objects = append(resp.LedgerObjects.Objects, &rpcv1.RawLedgerObject{
			Key:  cloneHash(key),
			Data: append([]byte(nil), data...),
		})
		count++
		return true
	}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, status.FromContextError(err).Err()
		}
		return nil, status.Errorf(codes.Internal, "iterating state: %v", err)
	}
	return resp, nil
}

// GetLedgerDiff returns the state-map differences between two ledgers. It
// leaves mod_type UNSPECIFIED on every entry; consumers infer
// CREATED / MODIFIED / DELETED from whether data is present (and, where they
// hold the base ledger, whether the key existed there).
func (s *Server) GetLedgerDiff(ctx context.Context, req *rpcv1.GetLedgerDiffRequest) (*rpcv1.GetLedgerDiffResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	base, err := s.resolveLedger(req.GetBaseLedger())
	if err != nil {
		return nil, err
	}
	desired, err := s.resolveLedger(req.GetDesiredLedger())
	if err != nil {
		return nil, err
	}

	diff, _, _, err := stateDiff(base, desired)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "comparing state maps: %v", err)
	}
	if !diff.Complete {
		return nil, status.Error(codes.ResourceExhausted, "too many differences between specified ledgers")
	}

	includeBlobs := req.GetIncludeBlobs()
	out := &rpcv1.GetLedgerDiffResponse{LedgerObjects: &rpcv1.RawLedgerObjects{}}
	for _, d := range diff.Differences {
		var desiredData []byte
		if d.SecondItem != nil {
			desiredData = d.SecondItem.Data()
		}
		out.LedgerObjects.Objects = append(out.LedgerObjects.Objects, diffEntry(d.Key, desiredData, includeBlobs))
	}
	return out, nil
}

// diffEntry builds a single RawLedgerObject for GetLedgerDiff: key is always
// set; data only when the entry exists in the desired ledger and the caller
// asked for blobs; mod_type is left UNSPECIFIED.
func diffEntry(key [32]byte, desiredData []byte, includeBlobs bool) *rpcv1.RawLedgerObject {
	obj := &rpcv1.RawLedgerObject{Key: cloneHash(key)}
	if includeBlobs && desiredData != nil {
		obj.Data = append([]byte(nil), desiredData...)
	}
	return obj
}

// maxStateDifferences bounds a state-map diff. The cap is effectively
// unreachable, so the incomplete-result path stays dead in practice.
const maxStateDifferences = math.MaxInt32

// stateDiff diffs base and desired ledgers' state maps, returning the
// difference set and the snapshots it was computed from (so callers can query
// neighbours). The snapshots share the immutable ledger nodes and Compare
// walks only the differing subtrees, so neither ledger is materialised in
// full.
func stateDiff(base, desired *ledger.Ledger) (*shamap.DifferenceSet, *shamap.SHAMap, *shamap.SHAMap, error) {
	baseMap, err := base.StateMapSnapshot()
	if err != nil {
		return nil, nil, nil, err
	}
	desiredMap, err := desired.StateMapSnapshot()
	if err != nil {
		return nil, nil, nil, err
	}
	diff, err := baseMap.Compare(desiredMap, maxStateDifferences)
	if err != nil {
		return nil, nil, nil, err
	}
	return diff, baseMap, desiredMap, nil
}

func (s *Server) specToIndex(spec *rpcv1.LedgerSpecifier) (string, error) {
	selection, err := selectorFromSpecifier(spec)
	if err != nil {
		return "", err
	}
	if selection.Kind() == ledgerselector.KindAbsent {
		return ledgerselector.Current().String(), nil
	}
	if selection.Kind() != ledgerselector.KindHash {
		return selection.String(), nil
	}
	result, err := s.resolveLedgerSelection(selection)
	if err != nil {
		return "", err
	}
	return ledgerselector.FromSequence(result.Sequence).String(), nil
}

// hash32 validates that input is exactly 32 bytes and copies it into a
// fixed-size array, reporting InvalidArgument with the field name otherwise.
func hash32(input []byte, field string) ([32]byte, error) {
	var h [32]byte
	if len(input) != 32 {
		return h, status.Errorf(codes.InvalidArgument, "%s must be 32 bytes, got %d", field, len(input))
	}
	copy(h[:], input)
	return h, nil
}

func cloneHash(h [32]byte) []byte {
	out := make([]byte, 32)
	copy(out, h[:])
	return out
}

func compareKey(a, b [32]byte) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

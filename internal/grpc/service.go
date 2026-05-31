// Package grpc implements the XRPLedgerAPIService gRPC surface mirroring
// rippled's binary-only ledger RPCs (the API surface consumed by Clio).
//
// References:
//   - rippled/include/xrpl/proto/org/xrpl/rpc/v1/xrp_ledger.proto
//   - rippled/src/xrpld/rpc/handlers/LedgerHandler.cpp (doLedgerGrpc)
//   - rippled/src/xrpld/rpc/handlers/GRPCHandlers.cpp
//
// The service delegates ledger lookups to the existing
// internal/ledger/service.Service so the gRPC and JSON-RPC surfaces stay
// behaviourally consistent.
package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
)

// LedgerLookup is the slice of the ledger Service that this gRPC
// implementation needs. Kept narrow so tests can substitute a fake.
type LedgerLookup interface {
	GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error)
	GetLedgerBySequence(seq uint32) (*ledger.Ledger, error)
	GetClosedLedger() *ledger.Ledger
	GetValidatedLedger() *ledger.Ledger
	GetOpenLedger() *ledger.Ledger
	GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*service.LedgerEntryResult, error)
}

type Server struct {
	rpcv1.UnimplementedXRPLedgerAPIServiceServer
	lookup LedgerLookup
}

func NewServer(lookup LedgerLookup) *Server {
	return &Server{lookup: lookup}
}

// resolveLedger maps a LedgerSpecifier to a concrete *ledger.Ledger.
// Mirrors rippled RPC::ledgerFromSpecifier (RPCHelpers.cpp:434-484):
//   - shortcut VALIDATED          → most recent validated ledger
//   - shortcut CLOSED             → most recent closed ledger
//   - shortcut CURRENT or         → open ledger
//     UNSPECIFIED (or nil spec)
//   - sequence/hash               → exact lookup
func (s *Server) resolveLedger(spec *rpcv1.LedgerSpecifier) (*ledger.Ledger, error) {
	if spec == nil {
		if l := s.lookup.GetOpenLedger(); l != nil {
			return l, nil
		}
		return nil, status.Error(codes.NotFound, "no open ledger available")
	}
	switch sel := spec.Ledger.(type) {
	case *rpcv1.LedgerSpecifier_Shortcut_:
		switch sel.Shortcut {
		case rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED:
			if l := s.lookup.GetValidatedLedger(); l != nil {
				return l, nil
			}
			return nil, status.Error(codes.NotFound, "no validated ledger available")
		case rpcv1.LedgerSpecifier_SHORTCUT_CLOSED:
			if l := s.lookup.GetClosedLedger(); l != nil {
				return l, nil
			}
			return nil, status.Error(codes.NotFound, "no closed ledger available")
		case rpcv1.LedgerSpecifier_SHORTCUT_CURRENT, rpcv1.LedgerSpecifier_SHORTCUT_UNSPECIFIED:
			if l := s.lookup.GetOpenLedger(); l != nil {
				return l, nil
			}
			return nil, status.Error(codes.NotFound, "no open ledger available")
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unknown ledger shortcut %v", sel.Shortcut)
		}
	case *rpcv1.LedgerSpecifier_Sequence:
		l, err := s.lookup.GetLedgerBySequence(sel.Sequence)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "ledger %d not found: %v", sel.Sequence, err)
		}
		return l, nil
	case *rpcv1.LedgerSpecifier_Hash:
		if len(sel.Hash) != 32 {
			return nil, status.Errorf(codes.InvalidArgument, "ledger hash must be 32 bytes, got %d", len(sel.Hash))
		}
		var h [32]byte
		copy(h[:], sel.Hash)
		l, err := s.lookup.GetLedgerByHash(h)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "ledger hash not found: %v", err)
		}
		return l, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "ledger specifier missing")
	}
}

// GetLedger mirrors rippled LedgerHandler.cpp doLedgerGrpc().
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
			list := &rpcv1.TransactionAndMetadataList{}
			if err := l.ForEachTransaction(func(_ [32]byte, data []byte) bool {
				// The txMap stores tx+metadata as a single VL-prefixed
				// blob. A proper Clio surface needs the
				// SHAMapTxNode-style split into separate transaction
				// and metadata Serializers; that lives one layer
				// deeper in the SHAMap encoding. Emit the combined
				// payload as transaction_blob for now; metadata_blob
				// stays empty until that helper is ported. See
				// rippled SHAMapItem layout and
				// LedgerHandler.cpp:140-146.
				list.Transactions = append(list.Transactions, &rpcv1.TransactionAndMetadata{
					TransactionBlob: append([]byte(nil), data...),
				})
				return true
			}); err != nil {
				return nil, status.Errorf(codes.Internal, "iterating transactions: %v", err)
			}
			resp.Transactions = &rpcv1.GetLedgerResponse_TransactionsList{TransactionsList: list}
		} else {
			hashes := &rpcv1.TransactionHashList{}
			if err := l.ForEachTransaction(func(h [32]byte, _ []byte) bool {
				out := make([]byte, 32)
				copy(out, h[:])
				hashes.Hashes = append(hashes.Hashes, out)
				return true
			}); err != nil {
				return nil, status.Errorf(codes.Internal, "iterating transactions: %v", err)
			}
			resp.Transactions = &rpcv1.GetLedgerResponse_HashesList{HashesList: hashes}
		}
	}

	if req.GetGetObjects() {
		// Computing a state-map diff between this ledger and its
		// parent requires SHAMap.compare(). Not exposed yet at the
		// goXRPL shamap layer; document the gap and surface it via
		// the proto's objects_included=false convention rather than
		// silently dropping the request.
		resp.ObjectsIncluded = false
	}

	return resp, nil
}

// GetLedgerEntry mirrors rippled LedgerEntry.cpp doLedgerEntryGrpc().
func (s *Server) GetLedgerEntry(ctx context.Context, req *rpcv1.GetLedgerEntryRequest) (*rpcv1.GetLedgerEntryResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if len(req.GetKey()) != 32 {
		return nil, status.Errorf(codes.InvalidArgument, "entry key must be 32 bytes, got %d", len(req.GetKey()))
	}

	ledgerIdx, err := s.specToIndex(req.GetLedger())
	if err != nil {
		return nil, err
	}

	var key [32]byte
	copy(key[:], req.GetKey())

	entry, err := s.lookup.GetLedgerEntry(ctx, key, ledgerIdx)
	if err != nil {
		if errors.Is(err, svcerr.ErrLedgerEntryNotFound) {
			return nil, status.Error(codes.NotFound, "ledger entry not found")
		}
		return nil, status.Errorf(codes.Internal, "lookup: %v", err)
	}

	return &rpcv1.GetLedgerEntryResponse{
		LedgerObject: &rpcv1.RawLedgerObject{
			Data: entry.Node,
			Key:  append([]byte(nil), key[:]...),
		},
		Ledger: req.GetLedger(),
	}, nil
}

// GetLedgerData iterates all state entries of a ledger, paginated by
// marker / end_marker. Mirrors rippled GRPCHandlers.cpp doLedgerDataGrpc().
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
		if len(m) != 32 {
			return nil, status.Errorf(codes.InvalidArgument, "marker must be 32 bytes, got %d", len(m))
		}
		copy(startKey[:], m)
		hasMarker = true
	}

	var endKey [32]byte
	hasEnd := false
	if m := req.GetEndMarker(); len(m) > 0 {
		if len(m) != 32 {
			return nil, status.Errorf(codes.InvalidArgument, "end_marker must be 32 bytes, got %d", len(m))
		}
		copy(endKey[:], m)
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

	passedMarker := !hasMarker
	count := 0
	var lastKey [32]byte
	pageFull := false
	if err := l.ForEachCtx(ctx, func(key [32]byte, data []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		if !passedMarker {
			if key == startKey {
				passedMarker = true
			}
			return true
		}
		if hasEnd && compareKey(key, endKey) >= 0 {
			return false
		}
		if count >= pageLimit {
			pageFull = true
			return false
		}
		resp.LedgerObjects.Objects = append(resp.LedgerObjects.Objects, &rpcv1.RawLedgerObject{
			Key:  cloneHash(key),
			Data: append([]byte(nil), data...),
		})
		lastKey = key
		count++
		return true
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "iterating state: %v", err)
	}
	if pageFull {
		resp.Marker = cloneHash(lastKey)
	}
	return resp, nil
}

// GetLedgerDiff returns the state-map differences between two ledgers.
// Without a fast SHAMap.compare helper at the goXRPL layer, we fall
// back to a streaming key-by-key comparison. Mirrors rippled
// LedgerDiff.cpp doLedgerDiffGrpc() — including its wire-shape choice
// of leaving `mod_type` UNSPECIFIED on every entry; consumers infer
// CREATED / MODIFIED / DELETED from whether `data` is present (and,
// where they have the base ledger, whether the key existed there).
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

	baseEntries, err := collectState(ctx, base)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "scanning base ledger: %v", err)
	}
	desiredEntries, err := collectState(ctx, desired)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "scanning desired ledger: %v", err)
	}

	includeBlobs := req.GetIncludeBlobs()
	out := &rpcv1.GetLedgerDiffResponse{LedgerObjects: &rpcv1.RawLedgerObjects{}}
	for key, desiredData := range desiredEntries {
		baseData, inBase := baseEntries[key]
		if inBase && bytesEqual(baseData, desiredData) {
			continue
		}
		out.LedgerObjects.Objects = append(out.LedgerObjects.Objects, diffEntry(key, desiredData, includeBlobs))
	}
	for key := range baseEntries {
		if _, ok := desiredEntries[key]; !ok {
			out.LedgerObjects.Objects = append(out.LedgerObjects.Objects, diffEntry(key, nil, false))
		}
	}
	return out, nil
}

// diffEntry builds a single RawLedgerObject for GetLedgerDiff in the
// rippled wire shape: `key` is always set; `data` is set only when the
// entry exists in the desired ledger AND the caller asked for blobs;
// `mod_type` is intentionally left UNSPECIFIED (see GetLedgerDiff).
func diffEntry(key [32]byte, desiredData []byte, includeBlobs bool) *rpcv1.RawLedgerObject {
	obj := &rpcv1.RawLedgerObject{Key: cloneHash(key)}
	if includeBlobs && desiredData != nil {
		obj.Data = append([]byte(nil), desiredData...)
	}
	return obj
}

func collectState(ctx context.Context, l *ledger.Ledger) (map[[32]byte][]byte, error) {
	out := make(map[[32]byte][]byte)
	err := l.ForEachCtx(ctx, func(key [32]byte, data []byte) bool {
		out[key] = append([]byte(nil), data...)
		return ctx.Err() == nil
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// specToIndex flattens a LedgerSpecifier into the string form expected by
// LedgerLookup.GetLedgerEntry. Hash-based specs are resolved through the
// lookup so callers can address a ledger by hash, matching rippled
// (RPCHelpers.cpp:445-450 + the explicit ledgerFromRequest template
// instantiation for GetLedgerEntryRequest at RPCHelpers.cpp:415-430).
func (s *Server) specToIndex(spec *rpcv1.LedgerSpecifier) (string, error) {
	if spec == nil {
		return "current", nil
	}
	switch sel := spec.Ledger.(type) {
	case *rpcv1.LedgerSpecifier_Shortcut_:
		switch sel.Shortcut {
		case rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED:
			return "validated", nil
		case rpcv1.LedgerSpecifier_SHORTCUT_CLOSED:
			return "closed", nil
		case rpcv1.LedgerSpecifier_SHORTCUT_CURRENT, rpcv1.LedgerSpecifier_SHORTCUT_UNSPECIFIED:
			return "current", nil
		default:
			return "", status.Errorf(codes.InvalidArgument, "unknown ledger shortcut %v", sel.Shortcut)
		}
	case *rpcv1.LedgerSpecifier_Sequence:
		return decimal(sel.Sequence), nil
	case *rpcv1.LedgerSpecifier_Hash:
		if len(sel.Hash) != 32 {
			return "", status.Errorf(codes.InvalidArgument, "ledger hash must be 32 bytes, got %d", len(sel.Hash))
		}
		var h [32]byte
		copy(h[:], sel.Hash)
		l, err := s.lookup.GetLedgerByHash(h)
		if err != nil {
			return "", status.Errorf(codes.NotFound, "ledger hash not found: %v", err)
		}
		return decimal(l.Sequence()), nil
	default:
		return "", status.Error(codes.InvalidArgument, "ledger specifier missing")
	}
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

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func decimal(n uint32) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

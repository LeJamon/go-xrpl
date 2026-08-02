// LedgerProvider implements peermanagement.LedgerProvider over
// *service.Service. It is wired into the overlay by NewFromConfig so
// peer-side replay, proof-path, and fetch-pack handlers can answer real
// requests instead of silently dropping them.
//
// This adapter lives in this layer (not in internal/peermanagement)
// because it needs to import internal/ledger and internal/ledger/service —
// imports the peermanagement layer is forbidden from making.
package adaptor

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// ledgerLookup is the minimal slice of *service.Service the provider needs.
// Pulling it behind an interface keeps the provider trivially unit-testable
// (no requirement to spin up a full service in every test) without expanding
// the production type's surface.
type ledgerLookup interface {
	GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error)
	EarliestFetch() uint32
}

type ledgerLookupContext interface {
	GetLedgerByHashContext(ctx context.Context, hash [32]byte) (*ledger.Ledger, error)
}

// MinimumOnlineFloor reports the lowest ledger sequence the node still retains
// in full. Ledgers below it have been (or are being) reclaimed by online-delete
// and must not be served — rippled gives the same guarantee implicitly because
// the store physically deleted them. *shamapstore.Rotator satisfies this. A nil
// floor (or a zero return) means online-delete is off / no rotation has happened
// yet, so nothing is withheld.
type MinimumOnlineFloor interface {
	MinimumOnline() uint32
}

// Compile-time interface check.
var _ peermanagement.LedgerProvider = (*LedgerProvider)(nil)

// LedgerProvider implements peermanagement.LedgerProvider on top of the
// go-xrpl ledger service. It answers the LedgerReplay protocol paths
// (mtREPLAY_DELTA_REQ / mtPROOF_PATH_REQ) and fetch-pack serving for the
// overlay. The mtGET_LEDGER path is NOT routed through this provider — the
// consensus router's handleGetLedger (router_serve.go) answers those
// requests directly from the ledger service. The adapter exists so
// peermanagement can reach the ledger service without importing
// internal/ledger, which is forbidden by the layering boundary between the
// two packages.
type LedgerProvider struct {
	svc   ledgerLookup
	floor MinimumOnlineFloor
}

// NewLedgerProvider constructs a LedgerProvider backed by the supplied
// ledger service. The returned value is safe for concurrent use because
// every call delegates to *service.Service, which carries its own
// synchronization.
func NewLedgerProvider(svc *service.Service) *LedgerProvider {
	return &LedgerProvider{svc: svc}
}

// SetMinimumOnlineFloor installs the online-delete retention floor. Once set,
// the provider refuses to serve ledgers below it (mirroring rippled, where a
// peer cannot serve what online-delete already removed). A nil floor leaves
// serving unrestricted, so the disabled / standalone path is unchanged.
func (p *LedgerProvider) SetMinimumOnlineFloor(floor MinimumOnlineFloor) {
	p.floor = floor
}

// belowFloor reports whether seq sits below the online-delete retention floor.
// A nil floor or a zero floor (no rotation yet) never withholds anything.
func (p *LedgerProvider) belowFloor(seq uint32) bool {
	if p.floor == nil {
		return false
	}
	floor := p.floor.MinimumOnline()
	return floor != 0 && seq < floor
}

// GetReplayDelta serves an mtREPLAY_DELTA_REQ:
//
//   - Look up the ledger by hash.
//   - Reject if it is unknown OR not yet immutable. Returning
//     (nil, nil, nil) is the LedgerProvider contract for
//     "unknown / not immutable", which the handler charges and drops.
//   - Otherwise return the serialized header and every tx leaf blob in
//     tx-map iteration order. Each leaf blob is a fresh copy: although
//     shamap.Item.Data() already copies, we double-copy via append so
//     the contract stays correct even if Item ever switches to returning
//     its internal slice.
func (p *LedgerProvider) GetReplayDelta(ledgerHash []byte) ([]byte, [][]byte, error) {
	return p.GetReplayDeltaContext(context.Background(), ledgerHash)
}

func (p *LedgerProvider) GetReplayDeltaContext(ctx context.Context, ledgerHash []byte) ([]byte, [][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	hash, ok := inbound.ToHash32(ledgerHash)
	if !ok {
		// Bad-length hash never matches a real ledger; treat as unknown.
		return nil, nil, nil
	}
	l, err := p.getLedgerContext(ctx, hash)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, err
	}
	if err != nil || l == nil || !l.IsImmutable() || p.belowFloor(l.Sequence()) {
		return nil, nil, nil
	}

	// Serialize the header WITHOUT its hash (includeHash=false): the
	// receiver recomputes the hash from the body and matches it against the
	// ledger_hash field of the response — including the hash here would
	// shift every subsequent byte and break that recompute.
	hdr := l.Header()
	headerBytes := header.AddRaw(hdr, false)

	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot tx map: %w", err)
	}

	var leaves [][]byte
	if err := txMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
		if ctx.Err() != nil {
			return false
		}
		raw := item.Data()
		leaves = append(leaves, append([]byte(nil), raw...))
		return true
	}); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("iterate tx map: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	return headerBytes, leaves, nil
}

// fetchPackMaxObjects caps the SHAMap nodes a single fetch-pack reply carries.
// Unlike rippled's have-diff, go-xrpl sends the want ledger's whole state+tx
// tree — its acquisition SHAMap has no node-hash store to supply un-sent shared
// nodes (see shamap.SHAMap.WalkFetchPackNodes). The cap bounds the reply: a
// moderate ledger fits in one pack, while a large (mainnet-scale) tree is
// truncated to a root-first connected prefix and the receiver completes the
// remainder via ordinary node-by-hash requests.
const fetchPackMaxObjects = 12288

// MakeFetchPack builds a fetch-pack for the parent of the ledger named by
// haveLedgerHash: the requester supplies a ledger hash it HAS, and we serve
// its predecessor ("want"). The reply carries want's header object (hash ==
// want's ledger hash) followed by its account-state and, when non-empty, its
// transaction SHAMap tree nodes, each tagged with want's sequence. It returns
// ErrFetchPackTooEarly below the configured serving range, or (nil, nil) when
// have is unknown, not yet immutable, or its parent is unavailable.
func (p *LedgerProvider) MakeFetchPack(haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error) {
	have, err := p.svc.GetLedgerByHash(haveLedgerHash)
	if err != nil || have == nil || !have.IsImmutable() {
		return nil, nil
	}
	if have.Sequence() < p.svc.EarliestFetch() {
		return nil, peermanagement.ErrFetchPackTooEarly
	}
	want, err := p.svc.GetLedgerByHash(have.Header().ParentHash)
	if err != nil || want == nil || p.belowFloor(want.Sequence()) {
		return nil, nil
	}
	if maxObjects <= 0 || maxObjects > fetchPackMaxObjects {
		maxObjects = fetchPackMaxObjects
	}

	seq := want.Sequence()
	wantHdr := want.Header()
	objects := make([]message.IndexedObject, 0, maxObjects)

	// Lead with the ledger-header object (HashPrefixLedgerMaster + raw
	// header). Its hash is want's ledger hash and sha512Half(data)
	// reproduces it, so a peer treats it as the pack's header node. go-xrpl
	// receivers already hold the header (via the acquisition's GotBase) and
	// simply ignore it.
	wantHash := want.Hash()
	headerData := append(protocol.HashPrefixLedgerMaster().Bytes(), header.AddRaw(wantHdr, false)...)
	objects = append(objects, message.IndexedObject{
		Hash:      append([]byte(nil), wantHash[:]...),
		Data:      headerData,
		LedgerSeq: seq,
	})

	stateMap, err := want.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot state map: %w", err)
	}
	objects, err = appendFetchPackNodes(objects, stateMap, maxObjects, seq)
	if err != nil {
		return nil, fmt.Errorf("walk state map: %w", err)
	}

	if wantHdr.TxHash != ([32]byte{}) {
		txMap, err := want.TxMapSnapshot()
		if err != nil {
			return nil, fmt.Errorf("snapshot tx map: %w", err)
		}
		objects, err = appendFetchPackNodes(objects, txMap, maxObjects, seq)
		if err != nil {
			return nil, fmt.Errorf("walk tx map: %w", err)
		}
	}

	return objects, nil
}

// appendFetchPackNodes walks up to the remaining-object budget of m's SHAMap
// tree nodes and appends each as a fetch-pack object tagged with seq.
func appendFetchPackNodes(objects []message.IndexedObject, m *shamap.SHAMap, maxObjects int, seq uint32) ([]message.IndexedObject, error) {
	remaining := maxObjects - len(objects)
	if remaining <= 0 {
		return objects, nil
	}
	nodes, err := m.WalkFetchPackNodes(remaining)
	if err != nil {
		return objects, err
	}
	for i := range nodes {
		objects = append(objects, message.IndexedObject{
			Hash:      append([]byte(nil), nodes[i].Hash[:]...),
			Data:      nodes[i].Data,
			LedgerSeq: seq,
		})
	}
	return objects, nil
}

// GetProofPath serves an mtPROOF_PATH_REQ:
//
//   - Ledger lookup must succeed; this path does NOT require immutability
//     (only mtREPLAY_DELTA_REQ does). Missing →
//     peermanagement.ErrLedgerNotFound.
//   - mapType selects the source SHAMap; an unsupported value yields a
//     generic error so the handler charges and drops. Defense in depth —
//     the handler itself rejects bad map types up front.
//   - Missing leaf → peermanagement.ErrKeyNotFound (the handler then
//     charges and drops without serializing a header).
//
// Path orientation is leaf-to-root, matching shamap.GetProofPath's wire
// ordering.
func (p *LedgerProvider) GetProofPath(
	ledgerHash []byte,
	key []byte,
	mapType message.LedgerMapType,
) ([]byte, [][]byte, error) {
	return p.GetProofPathContext(context.Background(), ledgerHash, key, mapType)
}

func (p *LedgerProvider) GetProofPathContext(
	ctx context.Context,
	ledgerHash []byte,
	key []byte,
	mapType message.LedgerMapType,
) ([]byte, [][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	hash, ok := inbound.ToHash32(ledgerHash)
	if !ok {
		return nil, nil, peermanagement.ErrLedgerNotFound
	}
	keyArr, ok := inbound.ToHash32(key)
	if !ok {
		// An unparseable key can have no matching leaf at this length.
		return nil, nil, peermanagement.ErrKeyNotFound
	}

	l, err := p.getLedgerContext(ctx, hash)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, err
	}
	if err != nil || l == nil || p.belowFloor(l.Sequence()) {
		return nil, nil, peermanagement.ErrLedgerNotFound
	}

	var snap *shamap.SHAMap
	switch mapType {
	case message.LedgerMapTransaction:
		snap, err = l.TxMapSnapshot()
	case message.LedgerMapAccountState:
		snap, err = l.StateMapSnapshot()
	default:
		return nil, nil, fmt.Errorf("unsupported map type %d", mapType)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot map: %w", err)
	}

	proof, err := snap.GetProofPathContext(ctx, keyArr)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get proof path: %w", err)
	}
	if proof == nil || !proof.Found {
		return nil, nil, peermanagement.ErrKeyNotFound
	}

	return l.SerializeHeader(), proof.Path, nil
}

func (p *LedgerProvider) getLedgerContext(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	if lookup, ok := p.svc.(ledgerLookupContext); ok {
		return lookup.GetLedgerByHashContext(ctx, hash)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return p.svc.GetLedgerByHash(hash)
}

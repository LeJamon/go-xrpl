// The ledger provider implements peermanagement.LedgerProvider over
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
	"time"

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
var _ peermanagement.LedgerProvider = (*ledgerProvider)(nil)

// ledgerProvider implements peermanagement.ledgerProvider on top of the
// go-xrpl ledger service. It answers the LedgerReplay protocol paths
// (mtREPLAY_DELTA_REQ / mtPROOF_PATH_REQ) and fetch-pack serving for the
// overlay. The mtGET_LEDGER path is NOT routed through this provider — the
// consensus router's handleGetLedger (router_serve.go) answers those
// requests directly from the ledger service. The adapter exists so
// peermanagement can reach the ledger service without importing
// internal/ledger, which is forbidden by the layering boundary between the
// two packages.
type ledgerProvider struct {
	svc          ledgerLookup
	floor        MinimumOnlineFloor
	loadedLocal  func() bool
	validatedAge func() time.Duration
}

// newLedgerProvider constructs a LedgerProvider backed by the supplied
// ledger service. The returned value is safe for concurrent use because
// every call delegates to *service.Service, which carries its own
// synchronization.
func newLedgerProvider(svc *service.Service) *ledgerProvider {
	return &ledgerProvider{
		svc:          svc,
		loadedLocal:  func() bool { return svc.FeeTrack() != nil && svc.FeeTrack().IsLoadedLocal() },
		validatedAge: svc.GetValidatedLedgerAge,
	}
}

// SetMinimumOnlineFloor installs the online-delete retention floor. Once set,
// the provider refuses to serve ledgers below it (mirroring rippled, where a
// peer cannot serve what online-delete already removed). A nil floor leaves
// serving unrestricted, so the disabled / standalone path is unchanged.
func (p *ledgerProvider) SetMinimumOnlineFloor(floor MinimumOnlineFloor) {
	p.floor = floor
}

// belowFloor reports whether seq sits below the online-delete retention floor.
// A nil floor or a zero floor (no rotation yet) never withholds anything.
func (p *ledgerProvider) belowFloor(seq uint32) bool {
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
func (p *ledgerProvider) GetReplayDelta(ledgerHash []byte) ([]byte, [][]byte, error) {
	return p.GetReplayDeltaContext(context.Background(), ledgerHash)
}

func (p *ledgerProvider) GetReplayDeltaContext(ctx context.Context, ledgerHash []byte) ([]byte, [][]byte, error) {
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

// fetchPackMaxObjects is LedgerMaster's historical stop condition: once a pack
// has 512 objects, traversal stops before adding an older ledger. The target
// ledger's state and transaction maps each have their own larger limits.
const (
	fetchPackMaxObjects    = 512
	fetchPackStateMaxNodes = 16384
	fetchPackTxMaxNodes    = 512
	fetchPackMaxAge        = time.Second
	fetchPackMaxBytes      = int64(60 * 1024 * 1024)
)

// MakeFetchPack builds a fetch-pack for the parent of the ledger named by
// haveLedgerHash: the requester supplies a ledger hash it HAS, and we serve
// its predecessor ("want"). The reply carries want's header object (hash ==
// want's ledger hash) followed by its account-state and, when non-empty, its
// transaction SHAMap tree nodes, each tagged with want's sequence. It returns
// ErrFetchPackTooEarly below the configured serving range, ErrFetchPackOpen
// for a known but open HAVE, ErrFetchPackBusy when local state is unavailable
// for construction, or (nil, nil) when have is unknown or its parent is
// unavailable.
func (p *ledgerProvider) MakeFetchPack(haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error) {
	return p.MakeFetchPackContext(context.Background(), haveLedgerHash, maxObjects)
}

func (p *ledgerProvider) MakeFetchPackContext(ctx context.Context, haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	explicitCap := maxObjects > 0
	if explicitCap && maxObjects > fetchPackMaxObjects {
		maxObjects = fetchPackMaxObjects
	}
	if p.loadedLocal != nil && p.loadedLocal() {
		return nil, peermanagement.ErrFetchPackBusy
	}
	if p.validatedAge != nil && p.validatedAge() > 40*time.Second {
		return nil, peermanagement.ErrFetchPackBusy
	}
	have, err := p.getLedgerContext(ctx, haveLedgerHash)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil || have == nil {
		return nil, nil
	}
	if !have.IsImmutable() {
		return nil, peermanagement.ErrFetchPackOpen
	}
	if have.Sequence() < p.svc.EarliestFetch() {
		return nil, peermanagement.ErrFetchPackTooEarly
	}
	capacity := fetchPackMaxObjects
	if explicitCap {
		capacity = maxObjects
	}
	objects := make([]message.IndexedObject, 0, capacity)
	currentHave := have
	want, err := p.getLedgerContext(ctx, have.Header().ParentHash)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil || want == nil || p.belowFloor(want.Sequence()) {
		return nil, nil
	}
	deadline := time.Now().Add(fetchPackMaxAge)
	remainingBytes := fetchPackMaxBytes

packLoop:
	for want != nil && (!explicitCap || len(objects) < maxObjects) && time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seq := want.Sequence()
		wantHdr := want.Header()
		wantHash := want.Hash()
		headerData := append(protocol.HashPrefixLedgerMaster().Bytes(), header.AddRaw(wantHdr, false)...)
		if int64(len(headerData)) > remainingBytes {
			break
		}
		objects = append(objects, message.IndexedObject{
			Hash:      append([]byte(nil), wantHash[:]...),
			Data:      headerData,
			LedgerSeq: seq,
		})
		remainingBytes -= int64(len(headerData))

		wantState, err := want.StateMapSnapshot()
		if err != nil {
			return nil, fmt.Errorf("snapshot state map: %w", err)
		}
		haveState, err := currentHave.StateMapSnapshot()
		if err != nil {
			return nil, fmt.Errorf("snapshot have state map: %w", err)
		}
		stateLimit := fetchPackStateMaxNodes
		if explicitCap {
			stateLimit = min(maxObjects-len(objects), stateLimit)
		}
		stateNodes, complete, err := wantState.WalkFetchPackDifferencesBounded(ctx, haveState, stateLimit, remainingBytes)
		if err != nil {
			return nil, fmt.Errorf("walk state map: %w", err)
		}
		objects = appendFetchPackNodesFromWalk(objects, stateNodes, seq)
		remainingBytes -= fetchPackNodesBytes(stateNodes)
		if !complete {
			break packLoop
		}

		if explicitCap && len(objects) >= maxObjects {
			break
		}
		if wantHdr.TxHash != ([32]byte{}) {
			txMap, err := want.TxMapSnapshot()
			if err != nil {
				return nil, fmt.Errorf("snapshot tx map: %w", err)
			}
			txLimit := fetchPackTxMaxNodes
			if explicitCap {
				txLimit = min(maxObjects-len(objects), txLimit)
			}
			txNodes, complete, err := txMap.WalkFetchPackNodesContextBounded(ctx, txLimit, remainingBytes)
			if err != nil {
				return nil, fmt.Errorf("walk tx map: %w", err)
			}
			objects = appendFetchPackNodesFromWalk(objects, txNodes, seq)
			remainingBytes -= fetchPackNodesBytes(txNodes)
			if !complete {
				break packLoop
			}
		}
		if explicitCap && len(objects) >= maxObjects {
			break
		}
		if len(objects) >= fetchPackMaxObjects {
			break
		}
		currentHave = want
		want, err = p.getLedgerContext(ctx, want.Header().ParentHash)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			// Historical traversal is opportunistic: once the requested
			// predecessor has been packed, a missing older ledger simply ends
			// the useful chain rather than failing the reply.
			break
		}
		if want != nil && p.belowFloor(want.Sequence()) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

func (p *ledgerProvider) getLedgerContext(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
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

func appendFetchPackNodesFromWalk(objects []message.IndexedObject, nodes []shamap.FetchPackNode, seq uint32) []message.IndexedObject {
	for i := range nodes {
		objects = append(objects, message.IndexedObject{
			Hash:      append([]byte(nil), nodes[i].Hash[:]...),
			Data:      nodes[i].Data,
			LedgerSeq: seq,
		})
	}
	return objects
}

func fetchPackNodesBytes(nodes []shamap.FetchPackNode) int64 {
	var total int64
	for i := range nodes {
		total += int64(len(nodes[i].Data))
	}
	return total
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
func (p *ledgerProvider) GetProofPath(
	ledgerHash []byte,
	key []byte,
	mapType message.LedgerMapType,
) ([]byte, [][]byte, error) {
	return p.GetProofPathContext(context.Background(), ledgerHash, key, mapType)
}

func (p *ledgerProvider) GetProofPathContext(
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

	return header.AddRaw(l.Header(), false), proof.Path, nil
}

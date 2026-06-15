// LedgerProvider implements peermanagement.LedgerProvider over
// *service.Service. It is wired into the overlay by NewFromConfig so
// peer-side ledger-sync handlers (mtREPLAY_DELTA_REQ, mtPROOF_PATH_REQ,
// mtGET_LEDGER) can answer real requests instead of silently dropping
// them.
//
// This adapter lives in this layer (not in internal/peermanagement)
// because it needs to import internal/ledger and internal/ledger/service —
// imports the peermanagement layer is forbidden from making.
package adaptor

import (
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
	GetLedgerBySequence(seq uint32) (*ledger.Ledger, error)
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

// GetLedgerHeader returns the serialized header for a ledger identified by
// hash (preferred) or, when no hash is supplied, by sequence. Returns
// (nil, nil) when the ledger is unknown or below the online-delete floor; a
// nil node means "no data to serve".
func (p *LedgerProvider) GetLedgerHeader(hash []byte, seq uint32) ([]byte, error) {
	l := p.lookupLedger(hash, seq)
	if l == nil || p.belowFloor(l.Sequence()) {
		return nil, nil
	}
	return l.SerializeHeader(), nil
}

// GetAccountStateNode returns the leaf data for nodeID in the account-state
// SHAMap of the ledger identified by ledgerHash. nodeID must be a 32-byte
// SHAMap key — partial-path SHAMapNodeID lookups are not supported here;
// peers that request them get an empty response, which the dispatcher treats
// the same as a missing node.
func (p *LedgerProvider) GetAccountStateNode(ledgerHash []byte, nodeID []byte) ([]byte, error) {
	l := p.lookupLedger(ledgerHash, 0)
	if l == nil || p.belowFloor(l.Sequence()) {
		return nil, nil
	}
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot state map: %w", err)
	}
	return lookupLeaf(stateMap, nodeID)
}

// GetTransactionNode mirrors GetAccountStateNode against the tx SHAMap.
func (p *LedgerProvider) GetTransactionNode(ledgerHash []byte, nodeID []byte) ([]byte, error) {
	l := p.lookupLedger(ledgerHash, 0)
	if l == nil || p.belowFloor(l.Sequence()) {
		return nil, nil
	}
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot tx map: %w", err)
	}
	return lookupLeaf(txMap, nodeID)
}

// GetReplayDelta serves an mtREPLAY_DELTA_REQ:
//
//   - Look up the ledger by hash.
//   - Reject if it is unknown OR not yet immutable. Returning
//     (nil, nil, nil) is the LedgerProvider contract for
//     "unknown / not immutable", which the handler maps to reNO_LEDGER.
//   - Otherwise return the serialized header and every tx leaf blob in
//     tx-map iteration order. Each leaf blob is a fresh copy: although
//     shamap.Item.Data() already copies, we double-copy via append so
//     the contract stays correct even if Item ever switches to returning
//     its internal slice.
func (p *LedgerProvider) GetReplayDelta(ledgerHash []byte) ([]byte, [][]byte, error) {
	hash, ok := inbound.ToHash32(ledgerHash)
	if !ok {
		// Bad-length hash never matches a real ledger; treat as unknown.
		return nil, nil, nil
	}
	l, err := p.svc.GetLedgerByHash(hash)
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
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		raw := item.Data()
		leaves = append(leaves, append([]byte(nil), raw...))
		return true
	}); err != nil {
		return nil, nil, fmt.Errorf("iterate tx map: %w", err)
	}

	return headerBytes, leaves, nil
}

// fetchPackMaxObjects caps the SHAMap nodes a single fetch-pack reply carries.
// go-xrpl packs a single ledger's FULL state+tx tree (it has no node-hash
// store to let a receiver supply un-sent shared nodes — see
// shamap.SHAMap.WalkFetchPackNodes), so the cap is sized to cover a moderate
// ledger's tree in one pack while still bounding the reply.
const fetchPackMaxObjects = 12288

// MakeFetchPack builds a fetch-pack for the parent of the ledger named by
// haveLedgerHash: the requester supplies a ledger hash it HAS, and we serve
// its predecessor ("want"). The reply carries want's header object (hash ==
// want's ledger hash) followed by its account-state and, when non-empty, its
// transaction SHAMap tree nodes, each tagged with want's sequence. Returns
// (nil, nil) — drop, no charge — when have is unknown, not yet immutable, or
// its parent is unavailable, matching the silent-drop stance of the other
// serve paths.
func (p *LedgerProvider) MakeFetchPack(haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error) {
	have, err := p.svc.GetLedgerByHash(haveLedgerHash)
	if err != nil || have == nil || !have.IsImmutable() {
		return nil, nil
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
	headerData := append(protocol.HashPrefixLedgerMaster.Bytes(), header.AddRaw(wantHdr, false)...)
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
//     generic error so the handler emits reBAD_REQUEST. Defense in depth —
//     the handler itself rejects bad map types up front.
//   - Missing leaf → peermanagement.ErrKeyNotFound (the handler then
//     returns reNO_NODE without serializing a header).
//
// Path orientation is leaf-to-root, matching shamap.GetProofPath's wire
// ordering.
func (p *LedgerProvider) GetProofPath(
	ledgerHash []byte,
	key []byte,
	mapType message.LedgerMapType,
) ([]byte, [][]byte, error) {
	hash, ok := inbound.ToHash32(ledgerHash)
	if !ok {
		return nil, nil, peermanagement.ErrLedgerNotFound
	}
	keyArr, ok := inbound.ToHash32(key)
	if !ok {
		// An unparseable key can have no matching leaf at this length.
		return nil, nil, peermanagement.ErrKeyNotFound
	}

	l, err := p.svc.GetLedgerByHash(hash)
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

	proof, err := snap.GetProofPath(keyArr)
	if err != nil {
		return nil, nil, fmt.Errorf("get proof path: %w", err)
	}
	if proof == nil || !proof.Found {
		return nil, nil, peermanagement.ErrKeyNotFound
	}

	return l.SerializeHeader(), proof.Path, nil
}

// lookupLedger resolves a ledger by its 32-byte hash when supplied,
// falling back to a sequence-based lookup. Returns nil on any miss so
// callers can shortcut to "no data for you" without surfacing the
// service's sentinel error.
func (p *LedgerProvider) lookupLedger(hash []byte, seq uint32) *ledger.Ledger {
	if h, ok := inbound.ToHash32(hash); ok {
		if l, err := p.svc.GetLedgerByHash(h); err == nil && l != nil {
			return l
		}
	}
	if seq != 0 {
		if l, err := p.svc.GetLedgerBySequence(seq); err == nil && l != nil {
			return l
		}
	}
	return nil
}

// lookupLeaf returns the data blob for a 32-byte SHAMap key. Non-32-byte
// nodeIDs (e.g. a path-based SHAMapNodeID) are not supported and yield
// (nil, nil), matching the dispatcher's "skip silently" behavior on missing
// nodes.
func lookupLeaf(snap *shamap.SHAMap, nodeID []byte) ([]byte, error) {
	key, ok := inbound.ToHash32(nodeID)
	if !ok {
		return nil, nil
	}
	item, found, err := snap.Get(key)
	if err != nil {
		return nil, fmt.Errorf("get leaf: %w", err)
	}
	if !found || item == nil {
		return nil, nil
	}
	raw := item.Data()
	return append([]byte(nil), raw...), nil
}

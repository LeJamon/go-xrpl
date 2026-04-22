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

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/shamap"
)

// ledgerLookup is the minimal slice of *service.Service the provider needs.
// Pulling it behind an interface keeps the provider trivially unit-testable
// (no requirement to spin up a full service in every test) without expanding
// the production type's surface.
type ledgerLookup interface {
	GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error)
	GetLedgerBySequence(seq uint32) (*ledger.Ledger, error)
}

// Compile-time interface check.
var _ peermanagement.LedgerProvider = (*LedgerProvider)(nil)

// LedgerProvider implements peermanagement.LedgerProvider on top of the
// goXRPL ledger service. It answers the LedgerReplay protocol paths
// (mtREPLAY_DELTA_REQ / mtPROOF_PATH_REQ) for the overlay; the legacy
// mtGET_LEDGER(LedgerInfoBase) path is NOT routed through this provider —
// the consensus router's handleGetLedger (router.go) answers those requests
// directly from the ledger service. The adapter exists so peermanagement
// can reach the ledger service without importing internal/ledger, which is
// forbidden by the layering boundary between the two packages.
type LedgerProvider struct {
	svc ledgerLookup
}

// NewLedgerProvider constructs a LedgerProvider backed by the supplied
// ledger service. The returned value is safe for concurrent use because
// every call delegates to *service.Service, which carries its own
// synchronization.
func NewLedgerProvider(svc *service.Service) *LedgerProvider {
	return &LedgerProvider{svc: svc}
}

// newLedgerProviderForTest builds a provider over an arbitrary lookup;
// only used by tests in this package.
func newLedgerProviderForTest(lookup ledgerLookup) *LedgerProvider {
	return &LedgerProvider{svc: lookup}
}

// GetLedgerHeader returns the serialized header for a ledger identified by
// hash (preferred) or, when no hash is supplied, by sequence. Returns
// (nil, nil) when the ledger is unknown — handleGetLedger interprets a nil
// node as "skip" and emits an empty response, matching rippled's silent
// drop on missing data.
func (p *LedgerProvider) GetLedgerHeader(hash []byte, seq uint32) ([]byte, error) {
	l := p.lookupLedger(hash, seq)
	if l == nil {
		return nil, nil
	}
	return l.SerializeHeader(), nil
}

// GetAccountStateNode returns the leaf data for nodeID in the account-state
// SHAMap of the ledger identified by ledgerHash. nodeID must be a 32-byte
// SHAMap key — partial-path SHAMapNodeID lookups (rippled's getNodeFat) are
// not supported here; peers that request them get an empty response, which
// the dispatcher treats the same as a missing node.
func (p *LedgerProvider) GetAccountStateNode(ledgerHash []byte, nodeID []byte) ([]byte, error) {
	l := p.lookupLedger(ledgerHash, 0)
	if l == nil {
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
	if l == nil {
		return nil, nil
	}
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot tx map: %w", err)
	}
	return lookupLeaf(txMap, nodeID)
}

// GetReplayDelta serves an mtREPLAY_DELTA_REQ. Mirrors rippled's
// LedgerReplayMsgHandler::processReplayDeltaRequest
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:179-219):
//
//   - Look up the ledger by hash.
//   - Reject if it is unknown OR not yet immutable. Per rippled :197
//     `if (!ledger || !ledger->isImmutable())`. Returning (nil, nil, nil)
//     mirrors the LedgerProvider contract for "unknown / not immutable",
//     which the handler maps to reNO_LEDGER.
//   - Otherwise return the serialized header and every tx leaf blob in
//     tx-map iteration order. Each leaf blob is a fresh copy: although
//     shamap.Item.Data() already copies, we double-copy via `append` so
//     the contract stays correct even if Item ever switches to returning
//     its internal slice.
func (p *LedgerProvider) GetReplayDelta(ledgerHash []byte) ([]byte, [][]byte, error) {
	hash, ok := toHash32(ledgerHash)
	if !ok {
		// Bad-length hash never matches a real ledger; mirror "unknown".
		return nil, nil, nil
	}
	l, err := p.svc.GetLedgerByHash(hash)
	if err != nil || l == nil || !l.IsImmutable() {
		return nil, nil, nil
	}

	// Mirror rippled's `addRaw(info, s)` at LedgerReplayMsgHandler.cpp:207
	// which leaves includeHash at its default (false). The receiver
	// recomputes the hash from the body and matches it against the
	// ledger_hash field of the response — including the hash here would
	// shift every subsequent byte and break that recompute.
	hdr := l.Header()
	headerBytes, hdrErr := header.AddRaw(hdr, false)
	if hdrErr != nil {
		return nil, nil, fmt.Errorf("serialize header: %w", hdrErr)
	}

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

// GetProofPath serves an mtPROOF_PATH_REQ. Mirrors rippled's
// LedgerReplayMsgHandler::processProofPathRequest
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:40-104):
//
//   - Ledger lookup must succeed; rippled does NOT require immutability for
//     this path (only mtREPLAY_DELTA_REQ does). Missing →
//     peermanagement.ErrLedgerNotFound.
//   - mapType selects the source SHAMap; an unsupported value yields a
//     generic error so the handler emits reBAD_REQUEST. Defense in depth —
//     the handler itself rejects bad map types up front.
//   - Missing leaf → peermanagement.ErrKeyNotFound, matching rippled's
//     "Don't have the node" branch at :84-90 (which returns reNO_NODE
//     without serializing a header — handleProofPathRequest mirrors that).
//
// Path orientation is leaf-to-root, matching both shamap.GetProofPath and
// rippled's SHAMap::getProofPath wire ordering (SHAMapSync.cpp:800-833).
func (p *LedgerProvider) GetProofPath(
	ledgerHash []byte,
	key []byte,
	mapType message.LedgerMapType,
) ([]byte, [][]byte, error) {
	hash, ok := toHash32(ledgerHash)
	if !ok {
		return nil, nil, peermanagement.ErrLedgerNotFound
	}
	keyArr, ok := toHash32(key)
	if !ok {
		// Mirror rippled's reNO_NODE for an unparseable key — there is no
		// matching leaf with this length.
		return nil, nil, peermanagement.ErrKeyNotFound
	}

	l, err := p.svc.GetLedgerByHash(hash)
	if err != nil || l == nil {
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
	if h, ok := toHash32(hash); ok {
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
// nodeIDs (e.g., rippled's path-based SHAMapNodeID) are not supported and
// yield (nil, nil), matching the dispatcher's "skip silently" behavior on
// missing nodes.
func lookupLeaf(snap *shamap.SHAMap, nodeID []byte) ([]byte, error) {
	key, ok := toHash32(nodeID)
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

// toHash32 returns h as a [32]byte array iff its length is exactly 32.
// The bool return distinguishes "wrong length" from "all-zero hash" so
// callers don't conflate parse failure with a legitimate sentinel value.
func toHash32(h []byte) ([32]byte, bool) {
	var out [32]byte
	if len(h) != len(out) {
		return out, false
	}
	copy(out[:], h)
	return out, true
}

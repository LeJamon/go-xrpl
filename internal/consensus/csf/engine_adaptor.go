package csf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
)

// This file wires csf's discrete-event scheduler and network to the REAL
// production consensus engine (internal/consensus/rcl). An EnginePeer
// implements consensus.Adaptor backed by simulation primitives and drives
// an rcl.Engine in ManualTick mode, so the csf suites exercise the actual
// state machine rather than a simulation-only re-implementation. The
// engine's clock is pinned to the scheduler's virtual time, making every
// run fully deterministic.

// simTxSet is a consensus.TxSet backed by opaque byte-blob transactions,
// kept keyed by tx hash so the set ID is a deterministic function of
// contents regardless of insertion order.
type simTxSet struct {
	txs map[consensus.TxID][]byte
}

func newSimTxSet(blobs [][]byte) *simTxSet {
	s := &simTxSet{txs: make(map[consensus.TxID][]byte, len(blobs))}
	for _, b := range blobs {
		s.txs[txBlobID(b)] = append([]byte(nil), b...)
	}
	return s
}

func txBlobID(b []byte) consensus.TxID {
	return consensus.TxID(sha256.Sum256(b))
}

func (s *simTxSet) sortedIDs() []consensus.TxID {
	ids := make([]consensus.TxID, 0, len(s.txs))
	for id := range s.txs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids
}

func (s *simTxSet) ID() consensus.TxSetID {
	h := sha256.New()
	for _, id := range s.sortedIDs() {
		h.Write(id[:])
	}
	var out consensus.TxSetID
	copy(out[:], h.Sum(nil))
	return out
}

func (s *simTxSet) Txs() [][]byte {
	out := make([][]byte, 0, len(s.txs))
	for _, id := range s.sortedIDs() {
		out = append(out, append([]byte(nil), s.txs[id]...))
	}
	return out
}

func (s *simTxSet) TxIDs() []consensus.TxID { return s.sortedIDs() }

func (s *simTxSet) Contains(id consensus.TxID) bool {
	_, ok := s.txs[id]
	return ok
}

func (s *simTxSet) Add(tx []byte) error {
	s.txs[txBlobID(tx)] = append([]byte(nil), tx...)
	return nil
}

func (s *simTxSet) Remove(id consensus.TxID) error {
	delete(s.txs, id)
	return nil
}

func (s *simTxSet) Size() int { return len(s.txs) }

func (s *simTxSet) Bytes() []byte {
	var buf bytes.Buffer
	for _, id := range s.sortedIDs() {
		blob := s.txs[id]
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(blob)))
		buf.Write(l[:])
		buf.Write(blob)
	}
	return buf.Bytes()
}

// simLedger is a consensus.Ledger whose ID is a pure function of its
// construction parameters, so every peer that builds from the same parent,
// tx-set and close time arrives at the same ledger hash without any shared
// state — the determinism csf relies on.
type simLedger struct {
	id        consensus.LedgerID
	seq       uint32
	parentID  consensus.LedgerID
	closeTime time.Time
	txSetID   consensus.TxSetID
}

func buildSimLedger(parentID consensus.LedgerID, seq uint32, txSetID consensus.TxSetID, closeTime time.Time) *simLedger {
	l := &simLedger{
		seq:       seq,
		parentID:  parentID,
		closeTime: closeTime,
		txSetID:   txSetID,
	}
	h := sha256.New()
	var b [8]byte
	binary.BigEndian.PutUint32(b[:4], l.seq)
	h.Write(b[:4])
	h.Write(l.parentID[:])
	h.Write(l.txSetID[:])
	binary.BigEndian.PutUint64(b[:], uint64(l.closeTime.UnixNano()))
	h.Write(b[:])
	copy(l.id[:], h.Sum(nil))
	return l
}

func (l *simLedger) ID() consensus.LedgerID       { return l.id }
func (l *simLedger) Seq() uint32                  { return l.seq }
func (l *simLedger) ParentID() consensus.LedgerID { return l.parentID }
func (l *simLedger) CloseTime() time.Time         { return l.closeTime }
func (l *simLedger) TxSetID() consensus.TxSetID   { return l.txSetID }
func (l *simLedger) Bytes() []byte                { return l.id[:] }

// simGenesis returns the genesis ledger shared by every peer. Pure
// function → identical across peers.
func simGenesis() *simLedger {
	empty := newSimTxSet(nil)
	return buildSimLedger(consensus.LedgerID{}, 0, empty.ID(), time.Unix(0, 0).UTC())
}

// EnginePeer is a simulation node that drives a real rcl.Engine. It
// implements consensus.Adaptor: ledger/tx storage is in-memory, network
// broadcasts schedule delivery to peers' engines through the csf network,
// signing/verification are no-ops (the sim trusts every message), and the
// clock is the scheduler's virtual time.
type EnginePeer struct {
	id     PeerID
	nodeID consensus.NodeID
	sched  *Scheduler
	net    *BasicNetwork
	reg    *engineRegistry
	engine *rcl.Engine

	trustedSet  []consensus.NodeID
	trustedHave map[consensus.NodeID]struct{}
	quorum      int
	granularity SimDuration

	mu        sync.Mutex
	opMode    consensus.OperatingMode
	ledgers   map[consensus.LedgerID]*simLedger
	bySeq     map[uint32]*simLedger
	lcl       consensus.Ledger
	validated consensus.LedgerID
	txSets    map[consensus.TxSetID]consensus.TxSet
	openTxs   [][]byte

	// fullyValidated records, in order, the ledgers this peer crossed the
	// trusted-validation quorum on — read after a run to assert the
	// validation pipeline ran end to end.
	fullyValidated []consensus.LedgerID
}

// engineRegistry resolves a PeerID to its EnginePeer so broadcasts can
// reach the destination engine's ingress methods.
type engineRegistry struct {
	mu    sync.RWMutex
	peers map[PeerID]*EnginePeer
}

func newEngineRegistry() *engineRegistry {
	return &engineRegistry{peers: make(map[PeerID]*EnginePeer)}
}

func (r *engineRegistry) add(p *EnginePeer) {
	r.mu.Lock()
	r.peers[p.id] = p
	r.mu.Unlock()
}

func (r *engineRegistry) get(id PeerID) *EnginePeer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peers[id]
}

// nodeIDFor derives a deterministic, unique consensus.NodeID from a PeerID.
func nodeIDFor(id PeerID) consensus.NodeID {
	var n consensus.NodeID
	binary.BigEndian.PutUint32(n[:4], uint32(id)+1)
	return n
}

// newEnginePeer constructs a peer and its engine. The engine is started in
// ManualTick mode with the scheduler's clock; the caller wires trust and
// begins ticking via begin().
func newEnginePeer(
	id PeerID,
	sched *Scheduler,
	net *BasicNetwork,
	reg *engineRegistry,
	trusted []consensus.NodeID,
	quorum int,
	granularity SimDuration,
	timing consensus.Timing,
) *EnginePeer {
	gen := simGenesis()
	p := &EnginePeer{
		id:          id,
		nodeID:      nodeIDFor(id),
		sched:       sched,
		net:         net,
		reg:         reg,
		trustedSet:  trusted,
		trustedHave: make(map[consensus.NodeID]struct{}, len(trusted)),
		quorum:      quorum,
		granularity: granularity,
		opMode:      consensus.OpModeFull,
		ledgers:     map[consensus.LedgerID]*simLedger{gen.id: gen},
		bySeq:       map[uint32]*simLedger{0: gen},
		lcl:         gen,
		txSets:      make(map[consensus.TxSetID]consensus.TxSet),
	}
	for _, n := range trusted {
		p.trustedHave[n] = struct{}{}
	}
	cfg := rcl.Config{
		Timing:     timing,
		Thresholds: consensus.DefaultThresholds(),
		Clock:      sched.NowTime,
		ManualTick: true,
	}
	p.engine = rcl.NewEngine(p, cfg)
	return p
}

// begin starts the engine, opens the first round, and schedules the
// recurring heartbeat tick on the scheduler.
func (p *EnginePeer) begin(ctx context.Context) error {
	if err := p.engine.Start(ctx); err != nil {
		return err
	}
	gen := p.lcl
	round := consensus.RoundID{Seq: gen.Seq() + 1, ParentHash: gen.ID()}
	if err := p.engine.StartRound(round, true); err != nil {
		return err
	}
	p.scheduleTick()
	return nil
}

func (p *EnginePeer) scheduleTick() {
	p.sched.In(p.granularity, func() {
		p.engine.TimerEntry()
		p.scheduleTick()
	})
}

func (p *EnginePeer) stop() { _ = p.engine.Stop() }

// --- consensus.NetworkBroadcaster ---

func (p *EnginePeer) deliverProposal(prop *consensus.Proposal) func(to PeerID) {
	return func(to PeerID) {
		if dst := p.reg.get(to); dst != nil {
			_ = dst.engine.OnProposal(prop, uint64(p.id))
		}
	}
}

func (p *EnginePeer) deliverValidation(val *consensus.Validation) func(to PeerID) {
	return func(to PeerID) {
		if dst := p.reg.get(to); dst != nil {
			_ = dst.engine.OnValidation(val, uint64(p.id))
		}
	}
}

func (p *EnginePeer) BroadcastProposal(prop *consensus.Proposal) error {
	cp := *prop
	p.net.Broadcast(p.id, p.deliverProposal(&cp))
	return nil
}

func (p *EnginePeer) BroadcastValidation(val *consensus.Validation) error {
	cp := *val
	p.net.Broadcast(p.id, p.deliverValidation(&cp))
	return nil
}

// RelayProposal/RelayValidation are no-ops: csf topologies the engine
// suite uses are fully connected, so every node already received the
// message directly from the originator's broadcast. The reduce-relay
// bookkeeping (UpdateRelaySlot / PeersThatHave) is a production overlay
// optimization the simulation does not model.
func (p *EnginePeer) RelayProposal(*consensus.Proposal, uint64) error     { return nil }
func (p *EnginePeer) RelayValidation(*consensus.Validation, uint64) error { return nil }
func (p *EnginePeer) UpdateRelaySlot([]byte, uint64, []uint64)            {}
func (p *EnginePeer) PeersThatHave([32]byte) []uint64                     { return nil }

// RequestTxSet serves the missing tx set from any peer that holds it,
// scheduled through the network from a connected peer; the requester's
// engine ingests it via OnTxSet (which uses the served blob). RequestLedger
// is a best-effort stub: the engine's OnLedger only adopts a ledger it
// already holds locally and ignores the served bytes, so genuine ledger
// catch-up is not modeled by the in-sync suites that use EnginePeer today.
func (p *EnginePeer) RequestTxSet(id consensus.TxSetID) error {
	for _, to := range p.net.Peers(p.id) {
		src := p.reg.get(to)
		if src == nil {
			continue
		}
		src.mu.Lock()
		ts, ok := src.txSets[id]
		src.mu.Unlock()
		if !ok {
			continue
		}
		txs := ts.Txs()
		p.net.Send(to, p.id, func() { _ = p.engine.OnTxSet(id, txs) })
		return nil
	}
	return nil
}

func (p *EnginePeer) RequestLedger(id consensus.LedgerID) error {
	for _, to := range p.net.Peers(p.id) {
		src := p.reg.get(to)
		if src == nil {
			continue
		}
		src.mu.Lock()
		l, ok := src.ledgers[id]
		src.mu.Unlock()
		if !ok {
			continue
		}
		blob := l.Bytes()
		p.net.Send(to, p.id, func() { _ = p.engine.OnLedger(id, blob) })
		return nil
	}
	return nil
}

// --- consensus.LedgerProvider ---

func (p *EnginePeer) GetLedger(id consensus.LedgerID) (consensus.Ledger, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.ledgers[id]; ok {
		return l, nil
	}
	return nil, errNotFound
}

func (p *EnginePeer) GetLedgerBySeq(seq uint32) (consensus.Ledger, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.bySeq[seq]; ok {
		return l, nil
	}
	return nil, errNotFound
}

func (p *EnginePeer) GetLastClosedLedger() (consensus.Ledger, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lcl, nil
}

func (p *EnginePeer) GetValidatedLedgerHash() consensus.LedgerID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validated
}

// GetMaxDisallowedLedgerSeq: simulated peers have no persisted pre-boot
// history, so the restart validation floor is always 0.
func (p *EnginePeer) GetMaxDisallowedLedgerSeq() uint32 {
	return 0
}

func (p *EnginePeer) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time, _ bool) (consensus.Ledger, error) {
	l := buildSimLedger(parent.ID(), parent.Seq()+1, txSet.ID(), closeTime)
	p.mu.Lock()
	p.ledgers[l.id] = l
	p.bySeq[l.seq] = l
	// Advance the LCL only on a strictly higher sequence: a same-seq
	// rebuild (e.g. a fork/re-org in a partition scenario) must not
	// side-grade the LCL to a different ledger ID, which would mask the
	// fork from GetLastClosedLedger-based assertions.
	if p.lcl == nil || l.seq > p.lcl.Seq() {
		p.lcl = l
	}
	p.txSets[txSet.ID()] = txSet
	p.mu.Unlock()
	return l, nil
}

func (p *EnginePeer) ValidateLedger(consensus.Ledger) error { return nil }

func (p *EnginePeer) StoreLedger(l consensus.Ledger) error {
	sl, ok := l.(*simLedger)
	if !ok {
		return nil
	}
	p.mu.Lock()
	p.ledgers[sl.id] = sl
	p.bySeq[sl.seq] = sl
	p.mu.Unlock()
	return nil
}

// --- consensus.TxPool ---

func (p *EnginePeer) GetPendingTxs() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneBlobs(p.openTxs)
}

func (p *EnginePeer) GetProposableTxs(consensus.Ledger) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneBlobs(p.openTxs)
}

func (p *EnginePeer) GenerateFlagLedgerPseudoTxs(consensus.Ledger, []*consensus.Validation) [][]byte {
	return nil
}
func (p *EnginePeer) GenerateNegativeUNLPseudoTx(consensus.Ledger) [][]byte { return nil }
func (p *EnginePeer) OnUNLChange(uint32, []consensus.NodeID)                {}

func (p *EnginePeer) GetTxSet(id consensus.TxSetID) (consensus.TxSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ts, ok := p.txSets[id]; ok {
		return ts, nil
	}
	return nil, errNotFound
}

func (p *EnginePeer) BuildTxSet(txs [][]byte) (consensus.TxSet, error) {
	ts := newSimTxSet(txs)
	p.mu.Lock()
	p.txSets[ts.ID()] = ts
	p.mu.Unlock()
	return ts, nil
}

func (p *EnginePeer) HasTx(id consensus.TxID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.openTxs {
		if txBlobID(b) == id {
			return true
		}
	}
	return false
}

func (p *EnginePeer) GetTx(id consensus.TxID) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.openTxs {
		if txBlobID(b) == id {
			return append([]byte(nil), b...), nil
		}
	}
	return nil, errNotFound
}

// --- consensus.ValidatorIdentity ---

func (p *EnginePeer) IsValidator() bool { return true }

func (p *EnginePeer) GetValidatorKey() (consensus.NodeID, error) { return p.nodeID, nil }

func (p *EnginePeer) SignProposal(prop *consensus.Proposal) error {
	prop.NodeID = p.nodeID
	prop.Signature = []byte{0x01}
	return nil
}

func (p *EnginePeer) SignValidation(val *consensus.Validation) error {
	val.NodeID = p.nodeID
	val.Signature = []byte{0x01}
	return nil
}

func (p *EnginePeer) VerifyProposal(*consensus.Proposal) error     { return nil }
func (p *EnginePeer) VerifyValidation(*consensus.Validation) error { return nil }

// --- consensus.TrustOracle ---

func (p *EnginePeer) IsTrusted(node consensus.NodeID) bool {
	_, ok := p.trustedHave[node]
	return ok
}

func (p *EnginePeer) GetTrustedValidators() []consensus.NodeID {
	out := make([]consensus.NodeID, len(p.trustedSet))
	copy(out, p.trustedSet)
	return out
}

func (p *EnginePeer) GetQuorum() int                     { return p.quorum }
func (p *EnginePeer) GetNegativeUNL() []consensus.NodeID { return nil }

func (p *EnginePeer) IsFeatureEnabled(string) bool                           { return true }
func (p *EnginePeer) IsFeatureEnabledOnLedger(consensus.Ledger, string) bool { return true }
func (p *EnginePeer) IsStandalone() bool                                     { return false }
func (p *EnginePeer) GetCookie() uint64                                      { return uint64(p.id) + 1 }
func (p *EnginePeer) GetServerVersion() uint64                               { return 0 }
func (p *EnginePeer) GetLoadFee() uint32                                     { return 0 }
func (p *EnginePeer) GetFeeVote() consensus.FeeVoteResult                    { return consensus.FeeVoteResult{} }
func (p *EnginePeer) GetAmendmentVote() [][32]byte                           { return nil }
func (p *EnginePeer) PeerReportedLedgers() []consensus.LedgerID              { return nil }

// --- consensus.TimeSource ---

func (p *EnginePeer) Now() time.Time { return p.sched.NowTime() }

func (p *EnginePeer) CloseTimeResolution() time.Duration { return time.Second }

func (p *EnginePeer) PrevCloseTimeResolution() time.Duration { return time.Second }

// AdjustCloseTime is intentionally a no-op: keeping Now() pinned to raw
// scheduler time (no drifting offset) means every peer reads an identical
// clock at each virtual instant, which is what makes the run deterministic
// and lets peers agree on close times.
func (p *EnginePeer) AdjustCloseTime(consensus.CloseTimes) {}

// --- consensus.StatusEvents ---

func (p *EnginePeer) GetOperatingMode() consensus.OperatingMode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opMode
}

func (p *EnginePeer) SetOperatingMode(mode consensus.OperatingMode) {
	p.mu.Lock()
	p.opMode = mode
	p.mu.Unlock()
}

func (p *EnginePeer) OnConsensusReached(consensus.Ledger, []*consensus.Validation, time.Duration) {}

func (p *EnginePeer) OnLedgerFullyValidated(ledgerID consensus.LedgerID, seq uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Only advance the validated tip on a strictly higher sequence so an
	// out-of-order callback (which OnLedger guards against under load)
	// cannot regress it.
	if cur, ok := p.ledgers[p.validated]; ok && seq <= cur.seq {
		return
	}
	p.validated = ledgerID
	p.fullyValidated = append(p.fullyValidated, ledgerID)
}

func (p *EnginePeer) OnModeChange(consensus.Mode, consensus.Mode)    {}
func (p *EnginePeer) OnPhaseChange(consensus.Phase, consensus.Phase) {}

// helpers

func cloneBlobs(in [][]byte) [][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make([][]byte, len(in))
	for i, b := range in {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

// Compile-time proof that EnginePeer is a full consensus.Adaptor.
var _ consensus.Adaptor = (*EnginePeer)(nil)

var errNotFound = errNotFoundErr{}

type errNotFoundErr struct{}

func (errNotFoundErr) Error() string { return "csf: object not found" }

package adaptor

import (
	"bytes"
	"math"
	"sort"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/amendmentvote"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

func (a *Adaptor) IsValidator() bool {
	return a.identity != nil
}

func (a *Adaptor) GetValidatorKey() (consensus.NodeID, error) {
	if a.identity == nil {
		return consensus.NodeID{}, ErrNoValidatorKey
	}
	return a.identity.NodeID, nil
}

// GetValidatorSigningKey returns the validator's 33-byte signing pubkey
// (ephemeral in token mode, master in seed-only mode) for validator_info /
// server_info. The 20-byte NodeID from GetValidatorKey must NOT be used here.
func (a *Adaptor) GetValidatorSigningKey() ([33]byte, error) {
	if a.identity == nil {
		return [33]byte{}, ErrNoValidatorKey
	}
	return a.identity.SigningKey, nil
}

func (a *Adaptor) SignProposal(proposal *consensus.Proposal) error {
	if a.identity == nil {
		return ErrNoValidatorKey
	}
	return a.identity.SignProposal(proposal)
}

func (a *Adaptor) SignValidation(validation *consensus.Validation) error {
	if a.identity == nil {
		return ErrNoValidatorKey
	}
	return a.identity.SignValidation(validation)
}

func (a *Adaptor) VerifyProposal(proposal *consensus.Proposal) error {
	return VerifyProposal(proposal)
}

func (a *Adaptor) VerifyValidation(validation *consensus.Validation) error {
	return VerifyValidation(validation)
}

func (a *Adaptor) IsTrusted(node consensus.NodeID) bool {
	a.trustUpdateMu.Lock()
	defer a.trustUpdateMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.trustedSet[node]
	return ok
}

// SetListedLookup installs the validator-list membership resolver
// (Aggregator.IsListed). Wired once at startup when publisher trust is
// configured; nil (the default) means nothing is listed.
func (a *Adaptor) SetListedLookup(fn func(consensus.NodeID) bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listedFn = fn
}

// IsListed implements consensus.ListedOracle: whether node appears in at
// least one live publisher list without (necessarily) being trusted.
func (a *Adaptor) IsListed(node consensus.NodeID) bool {
	a.mu.Lock()
	fn := a.listedFn
	a.mu.Unlock()
	return fn != nil && fn(node)
}

// RelayUntrustedValidations implements consensus.ValidationRelayPolicy:
// true under the default [relay_validations] "all" stance.
func (a *Adaptor) RelayUntrustedValidations() bool {
	return a.relayValidations == RelayValidationsAll
}

// DropUntrustedValidations reports the "drop_untrusted" stance; the router
// then sheds untrusted validations before signature verification.
func (a *Adaptor) DropUntrustedValidations() bool {
	return a.relayValidations == RelayValidationsDropUntrusted
}

func (a *Adaptor) OnTrustChanged(fn func([]consensus.NodeID, int)) {
	a.trustTransitionMu.Lock()
	defer a.trustTransitionMu.Unlock()

	a.trustUpdateMu.Lock()
	a.mu.Lock()
	a.onTrustChanged = fn
	a.mu.Unlock()
	a.trustUpdateMu.Unlock()

	if fn != nil {
		trusted, quorum := a.trustedValidatorsAndQuorum()
		fn(trusted, quorum)
	}
}

// OnTrustSettled registers a callback for the point after a trust transition
// callback has returned and the transition gate has reopened. If registration
// observes an already-settled snapshot, it invokes the callback once so a
// concurrent reload cannot leave stored evidence unexamined.
func (a *Adaptor) OnTrustSettled(fn func()) {
	a.trustTransitionMu.Lock()
	a.trustUpdateMu.Lock()
	a.mu.Lock()
	a.onTrustSettled = fn
	a.mu.Unlock()
	a.trustUpdateMu.Unlock()
	settled := fn != nil && !a.trustTransitioning.Load()
	a.trustTransitionMu.Unlock()
	if settled {
		fn()
	}
}

func (a *Adaptor) GetTrustedValidators() []consensus.NodeID {
	a.trustUpdateMu.Lock()
	defer a.trustUpdateMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]consensus.NodeID(nil), a.trustedValidators...)
}

// GetTrustedValidatorsAndQuorum returns an internally consistent snapshot.
func (a *Adaptor) GetTrustedValidatorsAndQuorum() ([]consensus.NodeID, int) {
	a.trustUpdateMu.Lock()
	defer a.trustUpdateMu.Unlock()
	return a.trustedValidatorsAndQuorum()
}

func (a *Adaptor) GetTrustedMasterKeys() [][33]byte {
	a.trustUpdateMu.Lock()
	defer a.trustUpdateMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([][33]byte, len(a.trustedMasterKeys))
	copy(result, a.trustedMasterKeys)
	return result
}

// SetTrustedValidators atomically replaces the operator-trusted validator set.
//
// validators and masterKeys are index-aligned and MUST be the same length; a
// mismatch is logged at WARN and the longer slice truncated (defensive only).
// Pass (nil, nil) to clear the set (standalone transition). The single entry
// point for every UNL-change trigger. Concurrency-safe; copies its inputs.
func (a *Adaptor) SetTrustedValidators(validators []consensus.NodeID, masterKeys [][33]byte) {
	if len(validators) != len(masterKeys) && (len(validators) > 0 || len(masterKeys) > 0) {
		a.logger.Warn("SetTrustedValidators: validators / masterKeys length mismatch; truncating to shorter",
			"validators_count", len(validators),
			"master_keys_count", len(masterKeys),
		)
		n := min(len(masterKeys), len(validators))
		validators = validators[:n]
		masterKeys = masterKeys[:n]
	}

	vCopy := make([]consensus.NodeID, len(validators))
	copy(vCopy, validators)
	newSet := make(map[consensus.NodeID]struct{}, len(validators))
	for _, v := range validators {
		newSet[v] = struct{}{}
	}
	var mkCopy [][33]byte
	if len(masterKeys) > 0 {
		mkCopy = make([][33]byte, len(masterKeys))
		copy(mkCopy, masterKeys)
	}

	a.trustTransitionMu.Lock()
	defer a.trustTransitionMu.Unlock()

	a.trustUpdateMu.Lock()
	a.trustTransitioning.Store(true)
	trustLocked := true
	defer func() {
		if trustLocked {
			a.trustUpdateMu.Unlock()
		}
		a.trustTransitioning.Store(false)
	}()

	negUNL := a.GetNegativeUNL()
	quorum := quorumForTrustedSet(newSet, len(vCopy), negUNL)
	if a.publisherQuorumUnavailable() {
		quorum = math.MaxInt
	}

	a.mu.Lock()
	a.trustedValidators = vCopy
	a.trustedSet = newSet
	a.trustedMasterKeys = mkCopy
	onTrustChanged := a.onTrustChanged
	onTrustSettled := a.onTrustSettled
	a.mu.Unlock()

	// trustedVotes is assigned once in New and never reassigned, so the
	// unlocked read is safe (TrustedVotes has its own mutex). Called after
	// releasing a.mu to avoid lock nesting.
	if a.trustedVotes != nil {
		a.trustedVotes.TrustChanged(vCopy)
	}
	if a.IsUNLBlocked() && a.GetOperatingMode() > consensus.OpModeConnected {
		a.SetOperatingMode(consensus.OpModeConnected)
	}
	// The tracker callback may synchronously call back into adaptor trust
	// readers. Release trustUpdateMu before dispatching it; the outer
	// transition mutex keeps a later setter from publishing out of order, and
	// the transition bit stays set until the matching tracker snapshot has been
	// installed.
	a.trustUpdateMu.Unlock()
	trustLocked = false
	if onTrustChanged != nil {
		onTrustChanged(vCopy, quorum)
	}
	// The tracker callback is intentionally run while the transition gate is
	// closed. Recheck once the matching snapshot is installed and the gate is
	// open so evidence collected during the transition can be promoted.
	a.trustTransitioning.Store(false)
	if onTrustSettled != nil {
		onTrustSettled()
	}
}

// GetQuorum returns the current quorum requirement, recomputed on
// every call to account for negative-UNL changes:
// max(ceil(0.8 * (trusted - disabled)), ceil(0.6 * trusted)).
func (a *Adaptor) GetQuorum() int {
	_, quorum := a.GetTrustedValidatorsAndQuorum()
	return quorum
}

// SetQuorumUnavailableFunc wires the publisher-availability quorum gate.
func (a *Adaptor) SetQuorumUnavailableFunc(fn func() bool) {
	a.quorumUnavailable = fn
}

func (a *Adaptor) publisherQuorumUnavailable() bool {
	return a.quorumUnavailable != nil && a.quorumUnavailable()
}

// IsQuorumUnavailable implements consensus.TrustOracle. The transition bit
// keeps finality closed until the tracker has installed the matching pair.
func (a *Adaptor) IsQuorumUnavailable() bool {
	return a.trustTransitioning.Load() || a.publisherQuorumUnavailable()
}

func (a *Adaptor) trustedValidatorsAndQuorum() ([]consensus.NodeID, int) {
	negUNL := a.GetNegativeUNL()
	unavailable := a.publisherQuorumUnavailable()

	a.mu.Lock()
	trusted := append([]consensus.NodeID(nil), a.trustedValidators...)
	trustedSet := a.trustedSet
	quorum := quorumForTrustedSet(trustedSet, len(trusted), negUNL)
	a.mu.Unlock()

	if unavailable {
		quorum = math.MaxInt
	}
	return trusted, quorum
}

func quorumForTrustedSet(trustedSet map[consensus.NodeID]struct{}, trusted int, negUNL []consensus.NodeID) int {
	disabled := 0
	for _, id := range negUNL {
		if _, ok := trustedSet[id]; ok {
			disabled++
		}
	}
	return computeQuorum(trusted, disabled)
}

// computeQuorum is the pure arithmetic behind GetQuorum: the minimum trusted,
// non-negUNL signatures to fully validate a ledger.
//
//   - standalone (trusted==0): 0 — no quorum gate.
//   - effective > 0: max(ceil(0.8 * effective), ceil(0.6 * trusted)). The 0.6
//     term is the AbsoluteMinimumQuorum floor (negative-UNL amendment) so a
//     large negUNL can't drop the bar below 60% of the full UNL.
//   - effective <= 0 (whole UNL on negUNL): math.MaxInt — an unreachable
//     quorum so no transient vote fires a spurious full-validation callback.
func computeQuorum(trusted, disabled int) int {
	if trusted == 0 {
		return 0
	}
	effective := trusted - disabled
	if effective <= 0 {
		return math.MaxInt
	}
	return max((effective*4+4)/5, (trusted*3+4)/5)
}

// disabledValidatorMasters reads the ltNEGATIVE_UNL SLE from the validated
// ledger and returns the 33-byte master pubkeys of disabled validators.
// Returns nil when there's no ledger service, no validated ledger, no SLE, or
// a malformed SLE (logged at warn, treated as empty). Bad entries are skipped.
func (a *Adaptor) disabledValidatorMasters() [][33]byte {
	l := a.validatedLedger()
	if l == nil {
		return nil
	}
	data, err := l.Read(keylet.NegativeUNL())
	if err != nil || len(data) == 0 {
		return nil
	}
	sle, err := pseudo.ParseNegativeUNLSLE(data)
	if err != nil {
		a.logger.Warn("failed to parse NegativeUNL SLE; treating as empty",
			"err", err,
			"seq", l.Sequence(),
		)
		return nil
	}
	if len(sle.DisabledValidators) == 0 {
		return nil
	}
	out := make([][33]byte, 0, len(sle.DisabledValidators))
	for _, dv := range sle.DisabledValidators {
		if len(dv.PublicKey) != 33 {
			continue
		}
		var master [33]byte
		copy(master[:], dv.PublicKey)
		out = append(out, master)
	}
	return out
}

// GetNegativeUNLMasters returns the 33-byte master pubkeys of disabled
// validators (raw, not the NodeIDs GetNegativeUNL returns). Used by the
// `validators` RPC.
func (a *Adaptor) GetNegativeUNLMasters() [][33]byte {
	return a.disabledValidatorMasters()
}

// GetNegativeUNL returns the NodeIDs of validators disabled on the validated
// ledger's ltNEGATIVE_UNL SLE. Returns nil when there's no ledger service, no
// validated ledger, no SLE, or a parse failure (logged at warn).
func (a *Adaptor) GetNegativeUNL() []consensus.NodeID {
	masters := a.disabledValidatorMasters()
	if masters == nil {
		return nil
	}
	// NegativeUNL stores 33-byte master keys; match against the 20-byte
	// calcNodeID(master) digest.
	out := make([]consensus.NodeID, 0, len(masters))
	for _, master := range masters {
		out = append(out, consensus.CalcNodeID(master))
	}
	return out
}

// GetCookie returns this adaptor's boot-lifetime cookie for emission
// via sfCookie on every outgoing validation.
func (a *Adaptor) GetCookie() uint64 {
	return a.cookie
}

// GetServerVersion returns the 64-bit sfServerVersion identifier. It avoids
// rippled's top bit (0x8000...) so go-xrpl isn't counted as rippled in peer
// version statistics.
func (a *Adaptor) GetServerVersion() uint64 {
	// Low bits reserved for a future semantic version; zero for now.
	return goxrplServerVersionTag
}

// GetLoadFee returns the local load_fee for outbound validations: the max of
// the local and cluster fee, or 0 ("omit") when that collapses to LoadBase.
func (a *Adaptor) GetLoadFee() uint32 {
	if a.ledgerService == nil {
		return 0
	}
	ft := a.ledgerService.FeeTrack()
	if ft == nil {
		return 0
	}
	fee := ft.LocalFee()
	if c := ft.ClusterFee(); c > fee {
		fee = c
	}
	if fee <= feetrack.LoadBase {
		return 0
	}
	return fee
}

// GetFeeVote returns the fee fields this validator should emit for ledger.
func (a *Adaptor) GetFeeVote(current consensus.Ledger) consensus.FeeVoteResult {
	result := consensus.FeeVoteResult{
		BaseFee:             a.feeVote.BaseFee,
		ReserveBase:         uint64(a.feeVote.ReserveBase),
		ReserveIncrement:    uint64(a.feeVote.ReserveIncrement),
		BaseFeeSet:          a.feeVote.BaseFeeSet,
		ReserveBaseSet:      a.feeVote.ReserveBaseSet,
		ReserveIncrementSet: a.feeVote.ReserveIncrementSet,
		PostXRPFees:         a.IsFeatureEnabled("XRPFees"),
	}

	wrapped, ok := current.(*LedgerWrapper)
	if !ok {
		return result
	}
	result.PostXRPFees = a.IsFeatureEnabledOnLedger(current, "XRPFees")

	data, err := wrapped.Unwrap().Read(keylet.Fees())
	if err != nil || len(data) == 0 {
		return result
	}
	fees, err := state.ParseFeeSettings(data)
	if err != nil {
		return result
	}
	result.BaseFeeSet = result.BaseFee != fees.GetBaseFee()
	result.ReserveBaseSet = result.ReserveBase != fees.GetReserveBase()
	result.ReserveIncrementSet = result.ReserveIncrement != fees.GetReserveIncrement()
	if !result.BaseFeeSet {
		result.BaseFee = 0
	}
	if !result.ReserveBaseSet {
		result.ReserveBase = 0
	}
	if !result.ReserveIncrementSet {
		result.ReserveIncrement = 0
	}
	return result
}

// currentAmendmentStances returns the validator's live per-amendment vote
// stances. With a live amendment table wired, stances are derived fresh from it
// (registry defaults, then operator veto → abstain, upvote → VoteUp) so changes
// take effect without restart; otherwise the construction-time map is returned.
func (a *Adaptor) currentAmendmentStances() map[[32]byte]amendmentvote.Stance {
	if a.amendmentTable == nil {
		return a.amendmentStances
	}
	stances := make(map[[32]byte]amendmentvote.Stance)
	for _, f := range amendment.AllFeatures() {
		switch {
		case f.Vote == amendment.VoteObsolete:
			stances[f.ID] = amendmentvote.VoteObsolete
		case a.amendmentTable.IsVetoed(f.ID):
			// vetoed → abstain (leave unset)
		case f.Supported == amendment.SupportedYes && a.amendmentTable.IsUpVoted(f.ID):
			// Operator upvote, supported amendments only.
			stances[f.ID] = amendmentvote.VoteUp
		case f.Supported == amendment.SupportedYes && f.Vote == amendment.VoteDefaultYes && !f.Retired:
			stances[f.ID] = amendmentvote.VoteUp
		}
	}
	return stances
}

// validatedLedger returns the most recent fully-validated ledger, or nil
// when no ledger service is wired or no ledger has been validated yet.
func (a *Adaptor) validatedLedger() *ledger.Ledger {
	if a.ledgerService == nil {
		return nil
	}
	return a.ledgerService.GetValidatedLedger()
}

// validatedRules returns the amendment Rules of the currently-validated
// ledger, or nil when no ledger service is wired or no ledger has been
// validated yet.
func (a *Adaptor) validatedRules() *amendment.Rules {
	if l := a.validatedLedger(); l != nil {
		return l.Rules()
	}
	return nil
}

// featureEnabled reports whether the named amendment is enabled in rules.
// unknownDefault is returned when rules is nil or the feature name is not
// recognised — lax (true) for the validation-broadcast path, strict
// (false) for engine-level gates.
func featureEnabled(rules *amendment.Rules, name string, unknownDefault bool) bool {
	if rules == nil {
		return unknownDefault
	}
	f := amendment.FeatureByName(name)
	if f == nil {
		return unknownDefault
	}
	return rules.Enabled(f.ID)
}

// GetAmendmentVote returns the amendment IDs this validator votes FOR on the
// next flag ledger, filtered against already-enabled amendments and canonically
// sorted (so equal stances yield byte-identical validations). nil when there's
// nothing to vote for.
func (a *Adaptor) GetAmendmentVote() [][32]byte {
	stances := a.currentAmendmentStances()
	if len(stances) == 0 {
		return nil
	}

	// Filter out amendments already enabled on the validated ledger. No
	// ledger/rules → nothing filtered (an un-synced node isn't validating).
	rules := a.validatedRules()

	out := make([][32]byte, 0, len(stances))
	for id, stance := range stances {
		if stance != amendmentvote.VoteUp {
			continue
		}
		if rules != nil && rules.Enabled(id) {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}

	// Canonical sort for byte-identical validations across validators.
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}

// IsFeatureEnabled reports whether the named amendment is enabled on the
// validated ledger's rules, gating optional STValidation fields (e.g.
// sfValidatedHash under featureHardenedValidations). Returns true on "unknown"
// (no service, pre-sync, or unrecognised name) as the safe mainnet default.
func (a *Adaptor) IsFeatureEnabled(name string) bool {
	return featureEnabled(a.validatedRules(), name, true)
}

// IsFeatureEnabledOnLedger reports whether the named amendment is enabled in
// the supplied ledger's own rules — a strict gate: any miss (nil ledger,
// unrecognised type, nil rules, unknown name) is "not enabled".
func (a *Adaptor) IsFeatureEnabledOnLedger(l consensus.Ledger, name string) bool {
	if l == nil {
		return false
	}
	w, ok := l.(*LedgerWrapper)
	if !ok {
		return false
	}
	return featureEnabled(w.Unwrap().Rules(), name, false)
}

// IsStandalone reports whether the node is configured for standalone
// (single-node) operation. Used by the engine to bypass the
// proposing-mode gate on flag-ledger pseudo-tx injection.
func (a *Adaptor) IsStandalone() bool {
	if a.ledgerService == nil {
		return false
	}
	return a.ledgerService.IsStandalone()
}

// SetUNLBlockedFunc wires the validator-list aggregator's lock-down flag
// into the consensus bow-out gate. Must be called before the engine starts.
func (a *Adaptor) SetUNLBlockedFunc(fn func() bool) {
	a.unlBlocked = fn
}

// IsUNLBlocked reports the validator-list UNL lock-down. Always false when
// no publisher lists are configured.
func (a *Adaptor) IsUNLBlocked() bool {
	if a.unlBlocked == nil {
		return false
	}
	return a.unlBlocked()
}

// SetUNLRefreshFunc wires the aggregator's per-round trust refresh. Must be
// called before the engine starts.
func (a *Adaptor) SetUNLRefreshFunc(fn func()) {
	a.refreshUNL = fn
}

// RefreshUNLState kicks off a live re-evaluation of the aggregator's trust
// view (promote rotations, latch/clear the lock-down flag) so the consensus
// bow-out reacts to an expiring list within a round or two instead of only on
// the standalone refresh tick. No-op without publisher lists.
func (a *Adaptor) RefreshUNLState() {
	if a.refreshUNL == nil {
		return
	}
	if !a.refreshInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.refreshInFlight.Store(false)
		a.refreshUNL()
	}()
}

// IsAmendmentBlocked reports whether an unsupported amendment has activated.
func (a *Adaptor) IsAmendmentBlocked() bool {
	if a.ledgerService == nil {
		return false
	}
	return a.ledgerService.IsAmendmentBlocked()
}

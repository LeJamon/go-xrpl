package service

import (
	"context"
	"errors"
	"fmt"
	"math/bits"

	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// SubmitResult contains the result of submitting a transaction
type SubmitResult struct {
	// Result is the engine result code
	Result ter.Result

	// Applied indicates if the transaction was applied to the ledger
	Applied bool

	// Fee is the fee charged (in drops)
	Fee uint64

	// Metadata contains the changes made by the transaction
	Metadata *tx.Metadata

	// Message is a human-readable result message
	Message string

	// CurrentLedger is the current open ledger sequence
	CurrentLedger uint32
	// CurrentLedgerCloseTime is the open-ledger close time in Ripple-epoch seconds.
	CurrentLedgerCloseTime int64

	// ValidatedLedger is the highest validated ledger sequence
	ValidatedLedger uint32

	// CurrentLedgerState is the immutable state snapshot captured at submit
	// time. It is nil when no validated ledger exists or the authoritative
	// account/fee state could not be derived.
	CurrentLedgerState *SubmitLedgerState
}

// SubmitLedgerState contains the current-ledger values returned by submit.
// The pointer on SubmitResult distinguishes an unavailable snapshot from
// valid zero-valued fields.
type SubmitLedgerState struct {
	ValidatedLedgerIndex     uint32
	OpenLedgerCost           uint64
	AccountSequenceNext      uint32
	AccountSequenceAvailable uint32
}

// SubmitTransaction is the RPC entry point for tx ingress. It mirrors
// rippled NetworkOPsImp::processTransaction → openLedger().modify
// (NetworkOPs.cpp:1483-1530): the submission is routed through
// TxQ.Apply (NetworkOPs.cpp:1518) so the fee-escalation queue holds
// transactions paying below the open-ledger fee level (terQUEUED) instead
// of applying them unconditionally. The held-pool then absorbs the blob
// unless the failure is permanent (tef*/tem*/tel*) — mirroring rippled's
// m_localTX->push_back at NetworkOPs.cpp:1677, which coexists with TxQ
// rather than being replaced by it. The legacy pendingTxs slice is fed
// for standalone close.
//
// This converges RPC ingress onto the same OpenLedger.SubmitDetailed →
// TxQ.Apply path the network-relay ingress (SubmitOpenLedgerTx) already
// uses, matching rippled where both routes share processTransaction.
//
// failHard mirrors rippled tapFAIL_HARD: when set, a submission that
// does not apply is NOT pushed into the localTxs held pool and is NOT
// fed into the canonical pendingTxs slice. The ApplyFlags also carries
// the bit so TxQ.canBeHeld rejects the queue admission (TxQ.cpp:393-399).
//
// Lock ordering: openLedgerMu serializes submission with ledger transitions;
// the service mutex is held only while capturing the current configuration and
// dependencies. SubmitDetailed then acquires the TxQ mutex without holding the
// service mutex.
func (s *Service) SubmitTransaction(transaction tx.Transaction, rawBlob []byte, failHard bool) (*SubmitResult, error) {
	s.mu.RLock()
	openLedgerView := s.openLedgerView
	s.mu.RUnlock()
	if openLedgerView == nil {
		return nil, svcerr.ErrNoOpenLedger
	}
	if rawBlob != nil {
		if err := tx.BindRawBytes(transaction, rawBlob); err != nil {
			return nil, err
		}
	}
	blob := transaction.GetRawBytes()

	s.mu.RLock()
	cfg, cfgErr := s.applyConfigLocked()
	standalone := s.config.Standalone
	s.mu.RUnlock()
	if cfgErr != nil {
		return nil, cfgErr
	}
	// RPC ingress skips signature verification in standalone mode (the
	// previous engine path did the same); the network path leaves it on.
	cfg.SkipSignatureVerification = standalone
	if failHard {
		cfg.ApplyFlags |= tx.TapFAIL_HARD
	}

	ptx, parseErr := openledger.ParsePendingTx(blob)
	if parseErr != nil {
		return &SubmitResult{
			Result:        ter.TemMALFORMED,
			Message:       ter.TemMALFORMED.Message(),
			CurrentLedger: openLedgerView.Current().Sequence(),
		}, nil
	}
	// Local submission checks (rippled STTx::passesLocalChecks via NetworkOPs):
	// memo size/charset limits are enforced on RPC and peer ingress, not in the
	// consensus-critical engine preflight. A transaction already admitted to a
	// consensus set remains governed only by consensus-critical checks.
	if localResult := tx.PassesTransactionLocalChecks(ptx.Parsed); localResult != ter.TesSUCCESS {
		return &SubmitResult{
			Result:        localResult,
			Message:       localResult.Message(),
			CurrentLedger: openLedgerView.Current().Sequence(),
		}, nil
	}
	preprocessValid := ptx.Parsed.GetCommon().GetFlags()&tx.TfInnerBatchTxn == 0
	// Verify the signature before SubmitDetailed acquires the apply mutex so the
	// in-strand check reuses the cached verdict (#1105). Skipped in standalone
	// mode, matching cfg.SkipSignatureVerification above.
	if preprocessValid && !cfg.SkipSignatureVerification {
		preprocessValid = txengine.PrewarmSignature(ptx.Parsed) == nil
	}

	if err := s.lockOpenLedgerIfRunning(openLedgerIngress); err != nil {
		return nil, err
	}
	defer s.openLedgerMu.Unlock()
	s.mu.RLock()
	openLedgerView = s.openLedgerView
	txQueue := s.txQueue
	localTxs := s.localTxs
	cfg, cfgErr = s.applyConfigLocked()
	standalone = s.config.Standalone
	s.mu.RUnlock()
	if openLedgerView == nil {
		return nil, svcerr.ErrNoOpenLedger
	}
	if cfgErr != nil {
		return nil, cfgErr
	}
	cfg.SkipSignatureVerification = standalone
	if failHard {
		cfg.ApplyFlags |= tx.TapFAIL_HARD
	}
	outcome := openLedgerView.SubmitDetailed(ptx, cfg, txQueue)

	current := openLedgerView.Current()
	currentSeq := current.Sequence()
	result := &SubmitResult{
		Result:        outcome.Result,
		Applied:       outcome.Applied,
		Fee:           outcome.Fee,
		Metadata:      outcome.Metadata,
		Message:       outcome.Message,
		CurrentLedger: currentSeq,
	}
	s.mu.RLock()
	validatedLedger := s.validatedLedger
	s.mu.RUnlock()
	if validatedLedger != nil {
		result.ValidatedLedger = validatedLedger.Sequence()
		result.CurrentLedgerState = s.submitLedgerState(current, ptx.Parsed, cfg, validatedLedger, txQueue)
	}
	if outcome.Class == openledger.ResultSuccess {
		s.rememberRelayTransaction(ptx.Hash, ptx.Blob, outcome.Queued)
	}

	// LocalTxs push: rippled NetworkOPs.cpp:1674-1683 holds a locally-
	// submitted tx whenever addLocal && !enforceFailHard, where
	// enforceFailHard = (fail_hard && result != tesSUCCESS). RPC submit is
	// always "local", so the hold condition reduces to
	// (!fail_hard || result == tesSUCCESS). rippled does NOT filter by TER
	// here — LocalTxsImp::push_back (LocalTxs.cpp:114-121) stores the blob
	// unconditionally, and LocalTxs::sweep ages out impossible/expired
	// entries after at most holdLedgers (5). So permanent failures
	// (tef/tem/tel) are held too and test-applied on each rebuilt open
	// ledger until they age out, matching rippled rather than pre-filtering
	// them. The held pool coexists with TxQ exactly as in rippled (LocalTxs
	// alongside TxQ).
	//
	// tefALREADY is the single exclusion: go-xrpl surfaces it from the
	// open-view pre-filter for a tx already in the view, so re-holding it is
	// pointless (sweep drops it next ledger via txExists).
	//
	// fail_hard short-circuits the hold on a non-applied tx: rippled's
	// enforceFailHard (NetworkOPs.cpp:1674) suppresses both this push (1677)
	// and relay (1685-1689) so the caller learns about the failure
	// immediately without a delayed re-application.
	if preprocessValid && rawBlob != nil && localTxs != nil {
		tr := outcome.Result
		if (!failHard || tr == ter.TesSUCCESS) && tr != ter.TefALREADY {
			localTxs.PushBack(currentSeq, ptx)
		}
	}

	// Standalone-mode close still drains pendingTxs
	// for the canonical re-sort. Append on apply so the legacy path
	// keeps working alongside the openLedgerView ingress.
	if outcome.Applied && rawBlob != nil {
		s.pendingTxs = append(s.pendingTxs, ptx)
	}

	s.dispatchProposedTransaction(ptx, ptx.Blob, outcome, current)
	if outcome.Applied {
		s.eventPublisher.dispatchServerStatusEvent()
	}

	return result, nil
}

// submitLedgerState derives the four submit-state fields from the same
// post-submit view and captured frontier dependencies. Any read or decode
// failure omits the complete snapshot rather than returning mixed state.
func (s *Service) submitLedgerState(
	current *ledger.Ledger,
	parsedTx tx.Transaction,
	cfg openledger.ApplyConfig,
	validatedLedger *ledger.Ledger,
	txQueue *txq.TxQ,
) *SubmitLedgerState {
	if current == nil || parsedTx == nil || validatedLedger == nil {
		return nil
	}

	common := parsedTx.GetCommon()
	if common == nil {
		return nil
	}
	accountID, err := state.DecodeAccountID(common.Account)
	if err != nil {
		return nil
	}
	account, err := state.ReadAccountRoot(current, accountID)
	if err != nil {
		return nil
	}
	accountSeq := uint32(0)
	if account != nil {
		accountSeq = account.Sequence
	}

	baseFee, reserveBase, reserveIncrement, err := readFeesFromLedgerContext(context.Background(), current)
	if err != nil {
		return nil
	}
	feeConfig := tx.EngineConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		LedgerSequence:            current.Sequence(),
		ParentCloseTime:           cfg.ParentCloseTime,
		ApplicationCloseTime:      cfg.ApplicationCloseTime,
		ApplicationCloseTimeSet:   cfg.ApplicationCloseTimeSet,
		ParentHash:                current.ParentHash(),
		NetworkID:                 s.config.NetworkID,
		SkipSignatureVerification: cfg.SkipSignatureVerification,
		ApplyFlags:                cfg.ApplyFlags,
		ViewOpen:                  cfg.Mode == openledger.OpenLedgerMode,
		Standalone:                s.config.Standalone,
		Rules:                     cfg.Rules,
		FeeTrack:                  s.feeTrack,
		Logger:                    s.config.Logger,
	}
	baseFeeForTx := computeBaseFeeForTx(current, parsedTx, feeConfig)
	availableSeq := accountSeq
	openLedgerCost := baseFeeForTx
	if txQueue != nil {
		feeAndSeq := txQueue.TxRequiredFeeAndSeq(accountID, accountSeq, baseFeeForTx, current.TxCount())
		openLedgerCost = feeAndSeq.RequiredFee
		availableSeq = feeAndSeq.AvailableSeq
	}

	return &SubmitLedgerState{
		ValidatedLedgerIndex:     validatedLedger.Sequence(),
		OpenLedgerCost:           openLedgerCost,
		AccountSequenceNext:      accountSeq,
		AccountSequenceAvailable: availableSeq,
	}
}

// dispatchProposedTransaction publishes only transactions committed to the
// open ledger. Callers hold s.openLedgerMu, which serializes local and peer
// ingress with ledger acceptance before the shared publication FIFO.
func (s *Service) dispatchProposedTransaction(
	ptx openledger.PendingTx,
	rawBlob []byte,
	outcome openledger.SubmitOutcome,
	current *ledger.Ledger,
) {
	if !s.eventPublisher.hasSubmittedTxCallback() || rawBlob == nil || !outcome.Applied || current == nil ||
		!mayPublishProposedTransaction(ptx.Parsed.GetCommon().GetFlags()) {
		return
	}
	ownerFunds, _, err := proposedOwnerFunds(rawBlob, current)
	if err != nil {
		s.logger.Error("failed to compute proposed transaction owner funds", "hash", fmt.Sprintf("%X", ptx.Hash), "err", err)
		return
	}
	s.eventPublisher.dispatchSubmittedTxEvent(SubmittedTxEvent{
		RawBlob:          append([]byte(nil), rawBlob...),
		TxHash:           ptx.Hash,
		AffectedAccounts: extractMentionedAccounts(rawBlob),
		CurrentLedger:    current.Sequence(),
		OwnerFunds:       ownerFunds,
		Result: Result{
			Code:    int(outcome.Result),
			Name:    outcome.Result.String(),
			Message: outcome.Message,
			Applied: outcome.Applied,
		},
	})
}

func mayPublishProposedTransaction(flags uint32) bool {
	return flags&tx.TfInnerBatchTxn == 0
}

// readFeesFromLedger reads fee settings from the FeeSettings SLE in the given
// ledger. It supports both the modern XRPFees format (BaseFeeDrops /
// ReserveBaseDrops / ReserveIncrementDrops) and the legacy format (BaseFee /
// ReserveBase / ReserveIncrement). Falls back to network defaults if the SLE
// cannot be found or parsed.
func readFeesFromLedger(l *ledger.Ledger) (baseFee, reserveBase, reserveIncrement uint64) {
	baseFee, reserveBase, reserveIncrement, err := readFeesFromLedgerContext(context.Background(), l)
	if err == nil {
		return baseFee, reserveBase, reserveIncrement
	}
	fees := drops.DefaultFees()
	if l != nil {
		fees = l.Fees()
	}
	return uint64(fees.Base), uint64(fees.Reserve), uint64(fees.Increment)
}

func readFeesFromLedgerContext(ctx context.Context, l *ledger.Ledger) (baseFee, reserveBase, reserveIncrement uint64, err error) {
	fees := drops.DefaultFees()
	if l == nil {
		return uint64(fees.Base), uint64(fees.Reserve), uint64(fees.Increment), nil
	}
	fees = l.Fees()

	data, err := l.ReadContext(ctx, keylet.Fees())
	if err != nil {
		return 0, 0, 0, err
	}
	if data == nil {
		return uint64(fees.Base), uint64(fees.Reserve), uint64(fees.Increment), nil
	}

	feeSettings, err := state.ParseFeeSettings(data)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse FeeSettings: %w", err)
	}
	fees = mergeFeeSettings(fees, feeSettings)
	return uint64(fees.Base), uint64(fees.Reserve), uint64(fees.Increment), nil
}

// FeesFromLedger returns the fee and reserve settings carried by a specific
// ledger, falling back to the protocol defaults when its FeeSettings entry is unavailable.
func FeesFromLedger(l *ledger.Ledger) (baseFee, reserveBase, reserveIncrement uint64) {
	return readFeesFromLedger(l)
}

// FeesFromLedgerStrict returns the fee settings carried by l while preserving
// storage and decoding failures. A missing FeeSettings entry uses the ledger's
// configured fee values.
func FeesFromLedgerStrict(l *ledger.Ledger) (baseFee, reserveBase, reserveIncrement uint64, err error) {
	return readFeesFromLedgerContext(context.Background(), l)
}

// GetCurrentFees returns the current fee settings read from the FeeSettings
// ledger entry in the open ledger. Falls back to hardcoded defaults if the
// open ledger is not available or the FeeSettings SLE cannot be read.
func (s *Service) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return readFeesFromLedger(s.openLedger)
}

// GetAutofillFee returns the Fee (drops) a transaction should carry to
// bypass the TxQ and enter the current open ledger. Mirrors rippled
// TransactionSign.cpp getCurrentNetworkFee (TransactionSign.cpp:839-877):
//
//   - feeDefault = per-tx-type base fee (multisign multiplier, AccountDelete's
//     reserve increment, AMMCreate's increment, LedgerStateFix's increment)
//   - loadFee   = scaleFeeLoad(feeDefault, feeTrack, isUnlimited)
//     (LoadFeeTrack.cpp:85-111) — inflates feeDefault under local /
//     cluster load; the unlimited carve-out lets admin/identified
//     callers pay the remote-rate factor while local load stays below
//     4x remote.
//   - escalatedFee = toDrops(openLedgerFeeLevel-1, baseFee) + 1 (TxQ load)
//   - returned fee = max(loadFee, escalatedFee)
//
// The returned fee is capped at feeDefault * mult / div (the caller's
// fee_mult_max / fee_div_max, default 10 / 1 per rippled Tuning.h);
// exceeding it yields *svcerr.HighFeeError (which
// errors.Is(svcerr.ErrHighFee) also matches). The ceiling check runs
// regardless of unlimited — rippled applies it after the role-aware
// scale, so privileged callers still cannot exceed mult/div.
//
// The source account is never read — matches rippled's getTxFee
// (TransactionSign.cpp:765-836), so callers that have already supplied
// Sequence must not receive an account-related error from this path.
func (s *Service) GetAutofillFee(parsedTx tx.Transaction, unlimited bool, mult, div int) (uint64, error) {
	if mult < 0 || div <= 0 {
		return 0, fmt.Errorf("autofill fee: invalid fee limit factors mult=%d div=%d", mult, div)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedgerView == nil {
		return 0, svcerr.ErrNoOpenLedger
	}
	current := s.openLedgerView.Current()
	if current == nil {
		return 0, svcerr.ErrNoOpenLedger
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(current)
	rules := current.Rules()
	if rules == nil {
		rules = s.currentOpenLedgerRulesLocked()
	}
	feeCfg := tx.EngineConfig{
		BaseFee:          baseFee,
		ReserveBase:      reserveBase,
		ReserveIncrement: reserveIncrement,
		NetworkID:        s.config.NetworkID,
		Rules:            rules,
	}

	feeDefault := baseFee
	if parsedTx != nil && tx.PassesTransactionLocalChecks(parsedTx) == ter.TesSUCCESS {
		feeDefault = computeBaseFeeForTx(current, parsedTx, feeCfg)
	}

	loadFee, scaleErr := feetrack.ScaleFeeLoad(feeDefault, s.feeTrack, unlimited)
	if scaleErr != nil {
		return 0, fmt.Errorf("autofill fee: %w", scaleErr)
	}
	fee := loadFee
	if s.txQueue != nil {
		feeLevel := s.txQueue.RequiredFeeLevel(current.TxCount())
		if uint64(feeLevel) > txq.BaseLevel {
			escalated := txq.FeeLevel(uint64(feeLevel)-1).ToDrops(baseFee) + 1
			if escalated > fee {
				fee = escalated
			}
		}
	}

	ceiling, ok := mulDivU64(feeDefault, uint64(mult), uint64(div))
	if !ok {
		return 0, fmt.Errorf("autofill fee: ceiling overflow (feeDefault=%d)", feeDefault)
	}
	if fee > ceiling {
		return 0, &svcerr.HighFeeError{Fee: fee, Limit: ceiling}
	}

	return fee, nil
}

// FeeTrack returns the LoadFeeTrack backing GetAutofillFee and the
// server_info load_factor_* fields. Used by Adaptor.OnLedgerFullyValidated
// (SetRemoteFee), the overlay TMCluster ingress sink (SetClusterFee),
// the per-close tick in processClosedLedgerLocked (Raise/LowerLocalFee),
// and the RPC LoadFactorFees hook.
func (s *Service) FeeTrack() *feetrack.LoadFeeTrack {
	return s.feeTrack
}

// GetAutofillSequence returns the Sequence a transaction should carry,
// reading the source account under the service RLock so it observes a
// consistent open-ledger snapshot. Mirrors rippled getAutofillSequence
// (Simulate.cpp:37-69):
//
//   - hasTicketSequence true → returns 0 unconditionally; missing account
//     does not error (the ticket itself supplies the sequence)
//   - otherwise reads the account SLE and consults TxQ.NextQueuableSeq so
//     the returned sequence accounts for already-queued transactions
//
// Returns svcerr.ErrAccountMalformed if the address fails to decode and
// svcerr.ErrAccountNotFound when the account is absent and no ticket
// supersedes the requirement.
func (s *Service) GetAutofillSequence(account string, hasTicketSequence bool) (uint32, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedger == nil {
		return 0, svcerr.ErrNoOpenLedger
	}

	_, accountIDBytes, decodeErr := addresscodec.DecodeClassicAddressToAccountID(account)
	if decodeErr != nil {
		return 0, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, decodeErr)
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	data, readErr := s.openLedger.Read(keylet.Account(accountID))
	if readErr != nil || data == nil {
		if hasTicketSequence {
			return 0, nil
		}
		return 0, svcerr.ErrAccountNotFound
	}

	if hasTicketSequence {
		return 0, nil
	}

	acct, parseErr := state.ParseAccountRoot(data)
	if parseErr != nil {
		return 0, fmt.Errorf("parse account root: %w", parseErr)
	}
	if s.txQueue != nil {
		return s.txQueue.NextQueuableSeq(accountID, acct.Sequence), nil
	}
	return acct.Sequence, nil
}

// computeBaseFeeForTx mirrors rippled getTxFee → calculateBaseFee dispatch:
// transaction-specific calculators and SetRegularKey's contextual waiver win;
// otherwise the default Transactor::calculateBaseFee applies, which charges
// one extra baseFee per entry in sfSigners regardless of SigningPubKey
// (rippled Transactor.cpp:229-245).
//
// Signer counts above STTx::maxMultiSigners fall back to baseFee,
// mirroring rippled's reference_fee fallback at
// TransactionSign.cpp:795-796. The cap is 32 by default and 8 only when
// cfg.Rules is supplied AND ExpandedSignerList is disabled — see
// maxMultiSigners and rippled STTx.h:55-63.
//
// Transaction-specific dispatch is wrapped in a recover so a panic
// reading inconsistent view state cannot escape the autofill path. This
// mirrors the reference_fee fallback rippled's getTxFee performs on any
// exception (TransactionSign.cpp:832-835).
func computeBaseFeeForTx(view tx.LedgerView, parsedTx tx.Transaction, cfg tx.EngineConfig) (fee uint64) {
	if parsedTx == nil {
		return cfg.BaseFee
	}
	_, batchFee := parsedTx.(tx.BatchFeeCalculator)
	_, customFee := parsedTx.(tx.CustomBaseFeeCalculator)
	txType := parsedTx.TxType()
	confidentialFee := txType == tx.TypeConfidentialMPTConvert ||
		txType == tx.TypeConfidentialMPTMergeInbox ||
		txType == tx.TypeConfidentialMPTConvertBack ||
		txType == tx.TypeConfidentialMPTSend ||
		txType == tx.TypeConfidentialMPTClawback
	if batchFee || customFee || confidentialFee || txType == tx.TypeRegularKeySet {
		defer func() {
			if r := recover(); r != nil {
				fee = cfg.BaseFee
			}
		}()
		return sign.CalculateBaseFee(parsedTx, view, cfg)
	}
	signerCount := len(parsedTx.GetCommon().Signers)
	if signerCount == 0 {
		return cfg.BaseFee
	}
	if signerCount > sign.MaxMultiSigners {
		return cfg.BaseFee
	}
	return sign.CalculateMultiSigFee(cfg.BaseFee, signerCount)
}

// mulDivU64 returns (a * b) / c; ok=false on uint64 overflow or c == 0.
func mulDivU64(a, b, c uint64) (uint64, bool) {
	if c == 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return 0, false
	}
	return lo / c, true
}

// EngineConfigForReplay returns the shared (non-per-ledger) engine
// configuration for replaying a closed ledger anchored on `parent`.
// Fees come from the parent's FeeSettings SLE — replay must use the
// fees that were active when the original txs ran. NetworkID and
// Logger come from the service config.
//
// The caller is expected to override the per-ledger fields
// (LedgerSequence, ParentCloseTime, ParentHash, Rules, ApplyFlags,
// OpenLedger) from the target header before passing this config to the
// engine. ReplayDelta.Apply() does this automatically.
//
// Reference: rippled BuildLedger.cpp uses the parent's view to source
// fees; per-ledger values are stamped from the closed-ledger info.
func (s *Service) EngineConfigForReplay(parent *ledger.Ledger) tx.EngineConfig {
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(parent)
	return tx.EngineConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		NetworkID:                 s.config.NetworkID,
		SkipSignatureVerification: false, // replay re-checks signatures
		Logger:                    s.config.Logger,
		Rules:                     rulesFromLedger(parent, s.logger),
	}
}

// TransactionResult contains a transaction and its metadata
type TransactionResult struct {
	TxData      []byte
	LedgerIndex uint32
	LedgerHash  [32]byte
	Validated   bool
	TxIndex     uint32
	CloseTime   int64
}

type LedgerContext struct {
	Hash      [32]byte
	CloseTime int64
}

// getOpenTransactionLocked looks up an accepted transaction in the
// authoritative open-ledger view. The caller must hold s.mu.RLock or s.mu.
// Open leaves are always transaction+metadata records; validate and split the
// complete leaf before exposing the transaction-only bytes to RPC callers.
func (s *Service) getOpenTransactionLocked(txHash [32]byte) (*TransactionResult, bool, error) {
	if s.openLedgerView == nil {
		return nil, false, nil
	}
	current := s.openLedgerView.Current()
	if current == nil {
		return nil, false, errors.New("open ledger view has no current ledger")
	}

	data, found, err := current.GetTransaction(txHash)
	if err != nil {
		return nil, false, fmt.Errorf("%w: get open-ledger transaction: %v", svcerr.ErrTxnDataCorrupt, err)
	}
	if !found {
		return nil, false, nil
	}

	accepted := ParseAcceptedTransaction(data)
	if err := accepted.ParseError(); err != nil {
		return nil, false, fmt.Errorf("%w: decode open-ledger transaction: %v", svcerr.ErrTxnDataCorrupt, err)
	}
	result, err := transactionResultFromRawBlob(accepted.TransactionBlob(), txHash, "open-ledger")
	if err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func transactionResultFromRawBlob(blob []byte, txHash [32]byte, source string) (*TransactionResult, error) {
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s transaction: %v", svcerr.ErrTxnDataCorrupt, source, err)
	}
	computedHash, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: hash %s transaction: %v", svcerr.ErrTxnDataCorrupt, source, err)
	}
	if computedHash != txHash {
		return nil, fmt.Errorf("%w: %s transaction hash mismatch: key=%x computed=%x", svcerr.ErrTxnDataCorrupt, source, txHash, computedHash)
	}
	txData, err := tx.EncodeWithVL(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: encode %s transaction: %v", svcerr.ErrTxnDataCorrupt, source, err)
	}
	return &TransactionResult{
		TxData: txData,
		// Transactions outside a closed ledger are not validated and have no
		// ledger metadata. Keep the closed-ledger fields at zero/invalid values.
		TxIndex: invalidTransactionIndex,
	}, nil
}

func (s *Service) getPendingTransaction(queue *txq.TxQ, locals *localtxs.LocalTxs, txHash [32]byte) (*TransactionResult, bool, error) {
	if queue != nil {
		if blob, ok := queue.GetTxBlob(txHash); ok {
			result, err := transactionResultFromRawBlob(blob, txHash, "queued")
			return result, true, err
		}
	}
	blob, _, _, ok := s.relayCacheGet(txHash)
	if ok {
		result, err := transactionResultFromRawBlob(blob, txHash, "relay-cache")
		return result, true, err
	}
	if locals != nil {
		if pending, ok := locals.Get(txHash); ok {
			result, err := transactionResultFromRawBlob(pending.Blob, txHash, "held")
			return result, true, err
		}
	}
	return nil, false, nil
}

// GetTransaction retrieves a transaction by its hash. The current open view
// is checked first, before historical/index lookups, matching rippled's
// transaction-cache behavior.
func (s *Service) GetTransaction(txHash [32]byte) (*TransactionResult, error) {
	s.mu.RLock()
	openResult, found, openErr := s.getOpenTransactionLocked(txHash)
	queue := s.txQueue
	locals := s.localTxs
	s.mu.RUnlock()
	if openErr != nil {
		return nil, openErr
	}
	if found {
		return openResult, nil
	}

	historyResult, historyErr := s.getHistoricalTransaction(txHash)
	if historyErr == nil {
		return historyResult, nil
	}
	if !errors.Is(historyErr, svcerr.ErrTxnNotFound) {
		return nil, historyErr
	}
	queuedResult, found, queuedErr := s.getPendingTransaction(queue, locals, txHash)
	if queuedErr != nil {
		return nil, queuedErr
	}
	if found {
		return queuedResult, nil
	}
	return nil, svcerr.ErrTxnNotFound
}

func (s *Service) getHistoricalTransaction(txHash [32]byte) (*TransactionResult, error) {
	s.historyComponent.mu.RLock()
	defer s.historyComponent.mu.RUnlock()

	// Look up which ledger contains this transaction
	ledgerSeq, found := s.txIndex[txHash]
	if !found {
		return nil, svcerr.ErrTxnNotFound
	}

	// Get the ledger
	l, ok := s.ledgerHistory[ledgerSeq]
	if !ok {
		return nil, svcerr.ErrLedgerNotFound
	}

	// Get the transaction data
	txData, found, err := l.GetTransaction(txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("%w: not found in ledger", svcerr.ErrTxnNotFound)
	}
	txIndex, ok := tx.TransactionIndexFromTxWithMetaBlob(txData)
	if !ok {
		txIndex = invalidTransactionIndex
	}

	return &TransactionResult{
		TxData:      txData,
		LedgerIndex: ledgerSeq,
		LedgerHash:  l.Hash(),
		Validated:   l.IsValidated(),
		TxIndex:     txIndex,
		CloseTime:   protocol.RippleSeconds(l.CloseTime()),
	}, nil
}

func (s *Service) GetLedgerContext(ctx context.Context, sequence uint32) (*LedgerContext, error) {
	if l, err := s.GetLedgerBySequence(sequence); err == nil && l != nil {
		return &LedgerContext{Hash: l.Hash(), CloseTime: protocol.RippleSeconds(l.CloseTime())}, nil
	}

	s.mu.RLock()
	db := s.relationalDB
	s.mu.RUnlock()
	if db == nil {
		return nil, svcerr.ErrLedgerNotFound
	}
	info, err := db.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(sequence))
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, svcerr.ErrLedgerNotFound
	}
	return &LedgerContext{
		Hash:      [32]byte(info.Hash),
		CloseTime: protocol.RippleSeconds(info.CloseTime),
	}, nil
}

// GetTransactionWithRange returns an unvalidated in-memory transaction before
// consulting the relational transaction table for the requested range.
func (s *Service) GetTransactionWithRange(ctx context.Context, txHash [32]byte, minLedger, maxLedger uint32) (*TransactionResult, relationaldb.TxSearchResult, error) {
	cached, cacheErr := s.GetTransaction(txHash)
	if cacheErr != nil && !errors.Is(cacheErr, svcerr.ErrTxnNotFound) {
		return nil, relationaldb.TxSearchUnknown, cacheErr
	}
	s.mu.RLock()
	db := s.relationalDB
	s.mu.RUnlock()
	if cacheErr == nil && !cached.Validated {
		return cached, relationaldb.TxSearchAll, nil
	}
	if db == nil || db.Transaction() == nil {
		return cached, relationaldb.TxSearchUnknown, cacheErr
	}

	dbResult, searched, err := db.Transaction().GetTransaction(ctx, relationaldb.Hash(txHash), &relationaldb.LedgerRange{
		Min: relationaldb.LedgerIndex(minLedger),
		Max: relationaldb.LedgerIndex(maxLedger),
	})
	if err != nil {
		return nil, searched, err
	}
	if dbResult == nil {
		return nil, searched, svcerr.ErrTxnNotFound
	}

	vlTx, err := tx.EncodeWithVL(dbResult.RawTxn)
	if err != nil {
		return nil, searched, fmt.Errorf("encode transaction length: %w", err)
	}
	vlMeta, err := tx.EncodeWithVL(dbResult.TxnMeta)
	if err != nil {
		return nil, searched, fmt.Errorf("encode transaction metadata length: %w", err)
	}
	txData := make([]byte, 0, len(vlTx)+len(vlMeta))
	txData = append(txData, vlTx...)
	txData = append(txData, vlMeta...)

	txIndex, ok := tx.TransactionIndexFromMetadata(dbResult.TxnMeta)
	if !ok {
		txIndex = invalidTransactionIndex
	}

	var ledgerHash [32]byte
	var closeTime int64
	validated := false
	if ledgerRepo := db.Ledger(); ledgerRepo != nil && dbResult.LedgerSeq != 0 {
		ledgerInfo, ledgerErr := ledgerRepo.GetLedgerInfoBySeq(ctx, dbResult.LedgerSeq)
		if ledgerErr != nil {
			return nil, searched, fmt.Errorf("get transaction ledger: %w", ledgerErr)
		}
		if ledgerInfo == nil {
			return nil, searched, errors.New("get transaction ledger: missing ledger header")
		}
		ledgerHash = [32]byte(ledgerInfo.Hash)
		closeTime = protocol.RippleSeconds(ledgerInfo.CloseTime)
		validated = dbResult.Status == "validated"
	}

	return &TransactionResult{
		TxData:      txData,
		LedgerIndex: uint32(dbResult.LedgerSeq),
		LedgerHash:  ledgerHash,
		Validated:   validated,
		TxIndex:     txIndex,
		CloseTime:   closeTime,
	}, searched, nil
}

// SimulateTransaction runs a transaction against a snapshot of the open ledger
// without committing changes. Returns the result and metadata.
func (s *Service) SimulateTransaction(transaction tx.Transaction) (*SubmitResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedgerView == nil {
		return nil, svcerr.ErrNoOpenLedger
	}
	current := s.openLedgerView.Current()
	if current == nil {
		return nil, svcerr.ErrNoOpenLedger
	}

	// Create a snapshot of the open ledger's state map for isolation
	snapshot, err := current.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("failed to create ledger snapshot: %w", err)
	}

	// Create a temporary ledger view backed by the snapshot
	simView := newSnapshotView(snapshot, current)

	// Read fee settings from the FeeSettings SLE in the open ledger
	simBaseFee, simReserveBase, simReserveIncrement := readFeesFromLedger(current)
	rules := current.Rules()
	if rules == nil {
		rules = s.currentOpenLedgerRulesLocked()
	}

	// Create engine config from current state
	engineConfig := tx.EngineConfig{
		BaseFee:          simBaseFee,
		ReserveBase:      simReserveBase,
		ReserveIncrement: simReserveIncrement,
		LedgerSequence:   current.Sequence(),
		ParentHash:       current.ParentHash(),
		OpenLedger:       true, // Check fee adequacy for simulation
		ApplyFlags:       tx.TapDRY_RUN,
		NetworkID:        s.config.NetworkID,
		Logger:           s.config.Logger,
		Rules:            rules,
		FeeTrack:         s.feeTrack,
	}

	// Create engine with the snapshot view
	engine := txengine.NewEngine(simView, engineConfig)

	// Apply the transaction (changes go to the snapshot, not the real ledger)
	applyResult := engine.Apply(transaction)

	result := &SubmitResult{
		Result:                 applyResult.Result,
		Applied:                applyResult.Applied,
		Fee:                    applyResult.Fee,
		Metadata:               applyResult.Metadata,
		Message:                applyResult.Message,
		CurrentLedger:          current.Sequence(),
		CurrentLedgerCloseTime: protocol.RippleSeconds(current.CloseTime()),
		ValidatedLedger:        0,
	}

	if s.validatedLedger != nil {
		result.ValidatedLedger = s.validatedLedger.Sequence()
	}

	return result, nil
}

// AccountTxResult contains the result of account_tx query
type AccountTxResult struct {
	Account      string                        `json:"account"`
	LedgerMin    uint32                        `json:"ledger_index_min"`
	LedgerMax    uint32                        `json:"ledger_index_max"`
	Limit        uint32                        `json:"limit"`
	Marker       *relationaldb.AccountTxMarker `json:"marker,omitempty"`
	Transactions []AccountTransaction          `json:"transactions"`
	Validated    bool                          `json:"validated"`
}

// AccountTransaction contains transaction data for account_tx
type AccountTransaction struct {
	Hash        [32]byte `json:"hash"`
	LedgerIndex uint32   `json:"ledger_index"`
	TxnSeq      uint32   `json:"txn_seq"`
	TxBlob      []byte   `json:"tx_blob,omitempty"`
	Meta        []byte   `json:"meta,omitempty"`
}

// UseTxTables reports whether the transaction tables backing the
// tx-history queries below are available, i.e. a relational DB is
// configured. Mirrors rippled config().useTxTables().
func (s *Service) UseTxTables() bool {
	return s.relationalDB != nil
}

// GetAccountTransactionsWithDelegate retrieves delegated transaction history
// for an account.
func (s *Service) GetAccountTransactionsWithDelegate(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *relationaldb.AccountTxMarker, forward bool, delegate *relationaldb.AccountTxDelegateFilter) (*AccountTxResult, error) {
	return s.getAccountTransactions(ctx, account, ledgerMin, ledgerMax, limit, marker, forward, delegate)
}

func (s *Service) getAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *relationaldb.AccountTxMarker, forward bool, delegate *relationaldb.AccountTxDelegateFilter) (*AccountTxResult, error) {
	// Snapshot the validated-seq bound under the lock, then release it before
	// the DB pages: relationalDB is immutable and the query needs no other
	// service state, so holding s.mu across the I/O would block consensus close
	// on a slow page. A slightly-stale validated bound is acceptable here.
	s.mu.RLock()
	hasValidated := s.validatedLedger != nil
	var validatedSeq uint32
	if hasValidated {
		validatedSeq = s.validatedLedger.Sequence()
	}
	s.mu.RUnlock()

	// If no RelationalDB, return error
	if s.relationalDB == nil {
		return nil, svcerr.ErrTxHistoryUnavailable
	}

	// Decode account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, err)
	}
	var accountID relationaldb.AccountID
	copy(accountID[:], accountIDBytes)

	// Set defaults
	if limit == 0 {
		limit = 200
	}

	// Determine ledger range.
	// When ledgerMin <= 0, use 1 (earliest possible ledger).
	// When ledgerMax <= 0, use the validated ledger sequence.
	minLedger := relationaldb.LedgerIndex(1)
	if ledgerMin > 0 {
		minLedger = relationaldb.LedgerIndex(ledgerMin)
	}

	var maxLedger relationaldb.LedgerIndex
	if ledgerMax > 0 {
		maxLedger = relationaldb.LedgerIndex(ledgerMax)
	} else if hasValidated {
		maxLedger = relationaldb.LedgerIndex(validatedSeq)
	} else {
		maxLedger = relationaldb.LedgerIndex(0xFFFFFFFF)
	}

	// Clamp max to validated ledger
	if hasValidated && maxLedger > relationaldb.LedgerIndex(validatedSeq) {
		maxLedger = relationaldb.LedgerIndex(validatedSeq)
	}

	options := relationaldb.AccountTxPageOptions{
		Account:   accountID,
		MinLedger: minLedger,
		MaxLedger: maxLedger,
		Marker:    marker,
		Limit:     limit,
		Delegate:  delegate,
	}

	var txResult *relationaldb.AccountTxResult
	if forward {
		txResult, err = s.relationalDB.AccountTransaction().GetOldestAccountTxsPage(ctx, options)
	} else {
		txResult, err = s.relationalDB.AccountTransaction().GetNewestAccountTxsPage(ctx, options)
	}
	if err != nil {
		return nil, err
	}

	// Convert to result
	result := &AccountTxResult{
		Account:      account,
		LedgerMin:    uint32(txResult.LedgerRange.Min),
		LedgerMax:    uint32(txResult.LedgerRange.Max),
		Limit:        txResult.Limit,
		Marker:       txResult.Marker,
		Transactions: make([]AccountTransaction, 0, len(txResult.Transactions)),
		Validated:    true,
	}

	for _, txInfo := range txResult.Transactions {
		result.Transactions = append(result.Transactions, AccountTransaction{
			Hash:        [32]byte(txInfo.Hash),
			LedgerIndex: uint32(txInfo.LedgerSeq),
			TxnSeq:      txInfo.TxnSeq,
			TxBlob:      txInfo.RawTxn,
			Meta:        txInfo.TxnMeta,
		})
	}

	return result, nil
}

// TxHistoryResult contains the result of tx_history query
type TxHistoryResult struct {
	Index        uint32               `json:"index"`
	Transactions []AccountTransaction `json:"txs"`
}

// GetTransactionHistory retrieves recent transactions.
// The supplied ctx is forwarded to the relational DB query.
func (s *Service) GetTransactionHistory(ctx context.Context, startIndex uint32) (*TxHistoryResult, error) {
	// relationalDB is set once at construction; this read-only query touches no
	// mutable service state, so it must not hold s.mu while the DB pages — a
	// slow page would otherwise block consensus close.
	if s.relationalDB == nil {
		return nil, svcerr.ErrTxHistoryUnavailable
	}

	txInfos, err := s.relationalDB.Transaction().GetTxHistory(ctx, relationaldb.LedgerIndex(startIndex), 20)
	if err != nil {
		return nil, err
	}

	result := &TxHistoryResult{
		Index:        startIndex,
		Transactions: make([]AccountTransaction, 0, len(txInfos)),
	}

	for _, txInfo := range txInfos {
		result.Transactions = append(result.Transactions, AccountTransaction{
			Hash:        [32]byte(txInfo.Hash),
			LedgerIndex: uint32(txInfo.LedgerSeq),
			TxBlob:      txInfo.RawTxn,
			Meta:        txInfo.TxnMeta,
		})
	}

	return result, nil
}

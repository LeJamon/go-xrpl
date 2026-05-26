package service

import (
	"context"
	"errors"
	"fmt"
	"math/bits"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/feetrack"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/txq"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// Autofill fee ceiling: feeDefault * mult / div. Mirrors rippled
// Tuning.h:60-61.
const (
	defaultAutoFillFeeMultiplier uint64 = 10
	defaultAutoFillFeeDivisor    uint64 = 1
)

// SubmitResult contains the result of submitting a transaction
type SubmitResult struct {
	// Result is the engine result code
	Result tx.Result

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

	// ValidatedLedger is the highest validated ledger sequence
	ValidatedLedger uint32
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
// Lock ordering: this holds s.mu while SubmitDetailed acquires the TxQ
// mutex via TxQ.Apply. The contract is s.mu → txQueue.mu (documented on
// both Service fields); TxQ methods never reach back for s.mu, so the
// submit and consensus-close paths cannot deadlock.
func (s *Service) SubmitTransaction(transaction tx.Transaction, rawBlob []byte, failHard bool) (*SubmitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openLedgerView == nil {
		return nil, ErrNoOpenLedger
	}
	if rawBlob != nil {
		transaction.SetRawBytes(rawBlob)
	}
	blob := rawBlob
	if blob == nil {
		blob = transaction.GetRawBytes()
	}

	cfg, cfgErr := s.applyConfigLocked()
	if cfgErr != nil {
		return nil, cfgErr
	}
	// RPC ingress skips signature verification in standalone mode (the
	// previous engine path did the same); the network path leaves it on.
	cfg.SkipSignatureVerification = s.config.Standalone
	if failHard {
		cfg.ApplyFlags |= tx.TapFAIL_HARD
	}

	ptx, parseErr := openledger.ParsePendingTx(blob)
	if parseErr != nil {
		return &SubmitResult{
			Result:        tx.TemMALFORMED,
			Message:       tx.TemMALFORMED.Message(),
			CurrentLedger: s.openLedgerView.Current().Sequence(),
		}, nil
	}

	outcome := s.openLedgerView.SubmitDetailed(ptx, cfg, s.txQueue)

	currentSeq := s.openLedgerView.Current().Sequence()
	result := &SubmitResult{
		Result:        outcome.Result,
		Applied:       outcome.Applied,
		Fee:           outcome.Fee,
		Metadata:      outcome.Metadata,
		Message:       outcome.Message,
		CurrentLedger: currentSeq,
	}
	if s.validatedLedger != nil {
		result.ValidatedLedger = s.validatedLedger.Sequence()
	}

	// LocalTxs push: rippled NetworkOPs.cpp:1677 holds every locally-
	// submitted tx that did not fail permanently. tef/tem/tel are
	// permanent failures; everything else (ter*/tec*/applied/terQUEUED)
	// belongs in the held pool so it survives Submit failure and LCL
	// transitions until it lands or ages out (5 ledgers). The held pool
	// coexists with TxQ exactly as in rippled (LocalTxs alongside TxQ).
	//
	// fail_hard short-circuits the held-pool push: rippled's TxQ
	// canBeHeld (TxQ.cpp:393-399) returns telCAN_NOT_QUEUE on
	// tapFAIL_HARD, and NetworkOPs.cpp:1685-1689 also gates relay on
	// !enforceFailHard. We translate that as "don't hold the blob" so
	// the caller learns about the failure immediately and doesn't see
	// a delayed re-application.
	if rawBlob != nil && s.localTxs != nil && !failHard {
		ter := outcome.Result
		if !ter.IsTef() && !ter.IsTem() && !ter.IsTel() && ter != tx.TefALREADY {
			s.localTxs.PushBack(currentSeq, ptx)
		}
	}

	// Standalone-mode close (AcceptLedgerAt) still drains pendingTxs
	// for the canonical re-sort. Append on apply so the legacy path
	// keeps working alongside the openLedgerView ingress.
	if outcome.Applied && rawBlob != nil {
		s.pendingTxs = append(s.pendingTxs, ptx)
	}

	// Fan out to the WebSocket transactions_proposed / accounts_proposed
	// publisher. Mirrors rippled NetworkOPs::processTransaction
	// (NetworkOPs.cpp:1535-1544) which calls pubProposedTransaction only
	// when the tx applied — tem/ter/tel failures that never touched the
	// open ledger are not announced. Mentioned accounts come from the
	// decoded blob so accounts_proposed fans to source, destination,
	// regular key, signers (mirrors STTx::getMentionedAccounts via the
	// existing extractor used on the validated transactions stream).
	if cb := s.submittedTxCallback; cb != nil && rawBlob != nil && outcome.Applied {
		ev := SubmittedTxEvent{
			RawBlob:          append([]byte(nil), rawBlob...),
			TxHash:           ptx.Hash,
			AffectedAccounts: extractAffectedAccounts(rawBlob),
			CurrentLedger:    currentSeq,
			Result: Result{
				Code:    int(outcome.Result),
				Name:    outcome.Result.String(),
				Message: outcome.Message,
				Applied: outcome.Applied,
			},
		}
		cb(ev)
	}

	return result, nil
}

// readFeesFromLedger reads fee settings from the FeeSettings SLE in the given
// ledger. It supports both the modern XRPFees format (BaseFeeDrops /
// ReserveBaseDrops / ReserveIncrementDrops) and the legacy format (BaseFee /
// ReserveBase / ReserveIncrement). Falls back to hardcoded defaults if the SLE
// cannot be found or parsed.
func readFeesFromLedger(l *ledger.Ledger) (baseFee, reserveBase, reserveIncrement uint64) {
	// Hardcoded defaults (same as rippled)
	const (
		defaultBaseFee          = 10
		defaultReserveBase      = 10_000_000
		defaultReserveIncrement = 2_000_000
	)

	if l == nil {
		return defaultBaseFee, defaultReserveBase, defaultReserveIncrement
	}

	data, err := l.Read(keylet.Fees())
	if err != nil || data == nil {
		return defaultBaseFee, defaultReserveBase, defaultReserveIncrement
	}

	feeSettings, err := state.ParseFeeSettings(data)
	if err != nil {
		return defaultBaseFee, defaultReserveBase, defaultReserveIncrement
	}

	return feeSettings.GetBaseFee(), feeSettings.GetReserveBase(), feeSettings.GetReserveIncrement()
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
// The returned fee is capped at feeDefault * defaultAutoFillFeeMultiplier
// / defaultAutoFillFeeDivisor; exceeding it yields *svcerr.HighFeeError
// (which errors.Is(svcerr.ErrHighFee) also matches). The ceiling check
// runs regardless of unlimited — rippled applies it after the role-aware
// scale, so privileged callers still cannot exceed mult/div.
//
// The source account is never read — matches rippled's getTxFee
// (TransactionSign.cpp:765-836), so callers that have already supplied
// Sequence must not receive an account-related error from this path.
func (s *Service) GetAutofillFee(parsedTx tx.Transaction, unlimited bool) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.openLedger)
	feeCfg := tx.EngineConfig{
		BaseFee:          baseFee,
		ReserveBase:      reserveBase,
		ReserveIncrement: reserveIncrement,
		NetworkID:        s.config.NetworkID,
		Rules:            rulesFromLedger(s.closedLedger, s.logger),
	}

	feeDefault := computeBaseFeeForTx(s.openLedger, parsedTx, feeCfg)

	loadFee, scaleErr := feetrack.ScaleFeeLoad(feeDefault, s.feeTrack, unlimited)
	if scaleErr != nil {
		return 0, fmt.Errorf("autofill fee: %w", scaleErr)
	}
	fee := loadFee
	if s.txQueue != nil {
		feeLevel := s.txQueue.GetRequiredFeeLevel(s.openLedger.TxCount())
		if uint64(feeLevel) > txq.BaseLevel {
			escalated := txq.FeeLevel(uint64(feeLevel)-1).ToDrops(baseFee) + 1
			if escalated > fee {
				fee = escalated
			}
		}
	}

	ceiling, ok := mulDivU64(feeDefault, defaultAutoFillFeeMultiplier, defaultAutoFillFeeDivisor)
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
		return 0, ErrNoOpenLedger
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
// CustomBaseFeeCalculator wins (AccountDelete, AMMCreate, LedgerStateFix);
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
// CustomBaseFeeCalculator dispatch is wrapped in a recover so a panic
// reading inconsistent view state cannot escape the autofill path. This
// mirrors the reference_fee fallback rippled's getTxFee performs on any
// exception (TransactionSign.cpp:832-835).
func computeBaseFeeForTx(view tx.LedgerView, parsedTx tx.Transaction, cfg tx.EngineConfig) (fee uint64) {
	if parsedTx == nil {
		return cfg.BaseFee
	}
	if feeCalc, ok := parsedTx.(tx.CustomBaseFeeCalculator); ok {
		defer func() {
			if r := recover(); r != nil {
				fee = cfg.BaseFee
			}
		}()
		return feeCalc.CalculateBaseFee(view, cfg)
	}
	signerCount := len(parsedTx.GetCommon().Signers)
	if signerCount == 0 {
		return cfg.BaseFee
	}
	if signerCount > maxMultiSigners(cfg.Rules) {
		return cfg.BaseFee
	}
	return tx.CalculateMultiSigFee(cfg.BaseFee, signerCount)
}

// maxMultiSigners mirrors rippled STTx::maxMultiSigners (STTx.h:55-63).
// rippled's contract is: "if rules are not supplied then the largest
// possible value is returned" — i.e. be permissive on nil so callers
// can't accidentally reject otherwise-valid signer arrays. Only when
// rules are supplied AND ExpandedSignerList is disabled does the cap
// fall to 8.
func maxMultiSigners(rules *amendment.Rules) int {
	if rules != nil && !rules.ExpandedSignerListEnabled() {
		return 8
	}
	return 32
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
}

// GetTransaction retrieves a transaction by its hash
func (s *Service) GetTransaction(txHash [32]byte) (*TransactionResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Look up which ledger contains this transaction
	ledgerSeq, found := s.txIndex[txHash]
	if !found {
		return nil, errors.New("transaction not found")
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
		return nil, errors.New("transaction not found in ledger")
	}

	return &TransactionResult{
		TxData:      txData,
		LedgerIndex: ledgerSeq,
		LedgerHash:  l.Hash(),
		Validated:   l.IsValidated(),
		TxIndex:     s.txPositionIndex[txHash],
	}, nil
}

// StoreTransaction stores a transaction in the current open ledger
func (s *Service) StoreTransaction(txHash [32]byte, txData []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openLedger == nil {
		return ErrNoOpenLedger
	}

	// Add to the open ledger's transaction map
	if err := s.openLedger.AddTransaction(txHash, txData); err != nil {
		return err
	}

	// Index the transaction to the current open ledger sequence
	s.txIndex[txHash] = s.openLedger.Sequence()

	return nil
}

// SimulateTransaction runs a transaction against a snapshot of the open ledger
// without committing changes. Returns the result and metadata.
func (s *Service) SimulateTransaction(transaction tx.Transaction) (*SubmitResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedger == nil {
		return nil, ErrNoOpenLedger
	}

	// Create a snapshot of the open ledger's state map for isolation
	snapshot, err := s.openLedger.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("failed to create ledger snapshot: %w", err)
	}

	// Create a temporary ledger view backed by the snapshot
	simView := newSnapshotView(snapshot, s.openLedger)

	// Read fee settings from the FeeSettings SLE in the open ledger
	simBaseFee, simReserveBase, simReserveIncrement := readFeesFromLedger(s.openLedger)

	// Create engine config from current state
	engineConfig := tx.EngineConfig{
		BaseFee:                   simBaseFee,
		ReserveBase:               simReserveBase,
		ReserveIncrement:          simReserveIncrement,
		LedgerSequence:            s.openLedger.Sequence(),
		SkipSignatureVerification: true, // Skip signatures for simulation
		OpenLedger:                true, // Check fee adequacy for simulation
		NetworkID:                 s.config.NetworkID,
		Logger:                    s.config.Logger,
		Rules:                     rulesFromLedger(s.closedLedger, s.logger),
	}

	// Create engine with the snapshot view
	engine := tx.NewEngine(simView, engineConfig)

	// Apply the transaction (changes go to the snapshot, not the real ledger)
	applyResult := engine.Apply(transaction)

	result := &SubmitResult{
		Result:          applyResult.Result,
		Applied:         applyResult.Applied,
		Fee:             applyResult.Fee,
		Metadata:        applyResult.Metadata,
		Message:         applyResult.Message,
		CurrentLedger:   s.openLedger.Sequence(),
		ValidatedLedger: 0,
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

// GetAccountTransactions retrieves transaction history for an account.
// The supplied ctx is forwarded to the relational DB query.
func (s *Service) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *relationaldb.AccountTxMarker, forward bool) (*AccountTxResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// If no RelationalDB, return error
	if s.relationalDB == nil {
		return nil, errors.New("transaction history not available (no database configured)")
	}

	// Decode account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, err)
	}
	var accountID relationaldb.AccountID
	copy(accountID[:], accountIDBytes)

	// Set defaults
	if limit == 0 || limit > 400 {
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
	} else if s.validatedLedger != nil {
		maxLedger = relationaldb.LedgerIndex(s.validatedLedger.Sequence())
	} else {
		maxLedger = relationaldb.LedgerIndex(0xFFFFFFFF)
	}

	// Clamp max to validated ledger
	if s.validatedLedger != nil && maxLedger > relationaldb.LedgerIndex(s.validatedLedger.Sequence()) {
		maxLedger = relationaldb.LedgerIndex(s.validatedLedger.Sequence())
	}

	options := relationaldb.AccountTxPageOptions{
		Account:   accountID,
		MinLedger: minLedger,
		MaxLedger: maxLedger,
		Marker:    marker,
		Limit:     limit,
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
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.relationalDB == nil {
		return nil, errors.New("transaction history not available (no database configured)")
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

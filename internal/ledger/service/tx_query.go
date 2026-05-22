package service

import (
	"context"
	"errors"
	"fmt"
	"math/bits"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/loadfeetrack"
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
// (NetworkOPs.cpp:1483-1530): every submission applies against the
// persistent OpenLedger view, the held-pool absorbs the blob unless the
// failure is permanent (tef*/tem*/tel*), and the legacy pendingTxs slice
// is fed for standalone close.
func (s *Service) SubmitTransaction(transaction tx.Transaction, rawBlob []byte) (*SubmitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openLedgerView == nil {
		return nil, ErrNoOpenLedger
	}
	if rawBlob != nil {
		transaction.SetRawBytes(rawBlob)
	}
	txHash, hashErr := tx.ComputeTransactionHash(transaction)
	if hashErr != nil {
		return nil, fmt.Errorf("compute tx hash: %w", hashErr)
	}

	var applyResult tx.ApplyResult
	applyResult.Result = tx.TefINTERNAL

	s.openLedgerView.Modify(func(view *ledger.Ledger) bool {
		if view.TxExists(txHash) {
			applyResult = tx.ApplyResult{Result: tx.TefALREADY, Message: tx.TefALREADY.Message()}
			return false
		}
		baseFee, reserveBase, reserveIncrement := readFeesFromLedger(view)
		engineConfig := tx.EngineConfig{
			BaseFee:                   baseFee,
			ReserveBase:               reserveBase,
			ReserveIncrement:          reserveIncrement,
			LedgerSequence:            view.Sequence(),
			SkipSignatureVerification: s.config.Standalone,
			OpenLedger:                true,
			NetworkID:                 s.config.NetworkID,
			Logger:                    s.config.Logger,
			Rules:                     rulesFromLedger(s.closedLedger, s.logger),
		}
		engine := tx.NewEngine(view, engineConfig)
		applyResult = engine.Apply(transaction)
		return applyResult.Applied
	})

	currentSeq := s.openLedgerView.Current().Sequence()
	result := &SubmitResult{
		Result:        applyResult.Result,
		Applied:       applyResult.Applied,
		Fee:           applyResult.Fee,
		Metadata:      applyResult.Metadata,
		Message:       applyResult.Message,
		CurrentLedger: currentSeq,
	}
	if s.validatedLedger != nil {
		result.ValidatedLedger = s.validatedLedger.Sequence()
	}

	common := transaction.GetCommon()
	var accountID [20]byte
	if common != nil {
		if _, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(common.Account); err == nil && len(accountBytes) == 20 {
			copy(accountID[:], accountBytes)
		}
	}

	// LocalTxs push: rippled NetworkOPs.cpp:1677 holds every locally-
	// submitted tx that did not fail permanently. tef/tem/tel are
	// permanent failures; everything else (ter*/tec*/applied/queued)
	// belongs in the held pool so it survives Submit failure and LCL
	// transitions until it lands or ages out (5 ledgers).
	if rawBlob != nil && s.localTxs != nil {
		ter := applyResult.Result
		if !ter.IsTef() && !ter.IsTem() && !ter.IsTel() && ter != tx.TefALREADY {
			ptx := openledger.PendingTx{
				Blob:    rawBlob,
				Hash:    txHash,
				Account: accountID,
			}
			if common != nil {
				ptx.Sequence = common.SeqProxy()
				ptx.IsTicket = common.TicketSequence != nil
			}
			s.localTxs.PushBack(currentSeq, ptx)
		}
	}

	// Standalone-mode close (AcceptLedgerAt) still drains pendingTxs
	// for the canonical re-sort. Append on apply so the legacy path
	// keeps working alongside the openLedgerView ingress.
	if applyResult.Applied && rawBlob != nil {
		ptx := pendingTx{
			Blob:    rawBlob,
			Hash:    txHash,
			Account: accountID,
		}
		if common != nil {
			ptx.Sequence = common.SeqProxy()
			ptx.IsTicket = common.TicketSequence != nil
		}
		s.pendingTxs = append(s.pendingTxs, ptx)
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
// TransactionSign.cpp getCurrentNetworkFee:
//
//   - feeDefault = per-tx-type base fee (multisign multiplier, AccountDelete's
//     reserve increment, AMMCreate's increment, LedgerStateFix's increment)
//   - escalatedFee = toDrops(openLedgerFeeLevel-1, baseFee) + 1 (TxQ load)
//   - returned fee = max(feeDefault, escalatedFee)
//
// The returned fee is capped at feeDefault * defaultAutoFillFeeMultiplier
// / defaultAutoFillFeeDivisor; exceeding it yields *svcerr.HighFeeError
// (which errors.Is(svcerr.ErrHighFee) also matches). rippled applies the
// ceiling regardless of role.
//
// The source account is never read — matches rippled's getTxFee
// (TransactionSign.cpp:765-836), so callers that have already supplied
// Sequence must not receive an account-related error from this path.
//
// scaleFeeLoad (rippled's load-fee tracker) is applied to feeDefault
// before the TxQ escalation comparison so a node reporting local /
// remote / cluster load inflates the autofilled fee in lockstep with
// rippled's TransactionSign.cpp:849-862. The isUnlimited(role)
// exemption is intentionally not threaded here: goXRPL has no
// admin-fee-bypass concept distinct from the ceiling check, so we
// always pass bUnlimited=false.
func (s *Service) GetAutofillFee(parsedTx tx.Transaction) (uint64, error) {
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
	fee := feeDefault
	if s.loadFeeTrack != nil {
		if scaled, ok := loadfeetrack.ScaleFee(feeDefault, s.loadFeeTrack, false); ok {
			fee = scaled
		}
	}
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

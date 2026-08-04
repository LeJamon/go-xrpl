package txq

import (
	"bytes"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/xchain"
)

type flaggedAdmissionContext struct {
	*stubApplyCtx
	rules              *amendment.Rules
	preflightFlags     []tx.ApplyFlags
	preclaimFlags      []tx.ApplyFlags
	applyFlags         []tx.ApplyFlags
	preflightResult    ter.Result
	preclaimResult     ter.Result
	applyResult        ter.Result
	applyWasSuccessful bool
}

func (c *flaggedAdmissionContext) RulesIdentity() *amendment.Rules { return c.rules }

func (c *flaggedAdmissionContext) PreflightTransactionWithFlags(_ tx.Transaction, flags tx.ApplyFlags) ter.Result {
	c.preflightFlags = append(c.preflightFlags, flags)
	return c.preflightResult
}

func (c *flaggedAdmissionContext) PreclaimTransactionWithFlags(_ tx.Transaction, _ [20]byte, _ uint64, _ uint32, flags tx.ApplyFlags) ter.Result {
	c.preclaimFlags = append(c.preclaimFlags, flags)
	return c.preclaimResult
}

func (c *flaggedAdmissionContext) ApplyTransactionWithFlags(_ tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool) {
	c.applyFlags = append(c.applyFlags, flags)
	return c.applyResult, c.applyWasSuccessful
}

type acceptFlagsContext struct {
	rules              *amendment.Rules
	preflightFlags     []tx.ApplyFlags
	applyFlags         []tx.ApplyFlags
	preflightResult    ter.Result
	applyResult        ter.Result
	applyWasSuccessful bool
	readFn             func()
}

func (c *acceptFlagsContext) observeRead() {
	if c.readFn != nil {
		c.readFn()
	}
}
func (c *acceptFlagsContext) RulesIdentity() *amendment.Rules {
	c.observeRead()
	return c.rules
}
func (c *acceptFlagsContext) GetTxInLedger() uint32 {
	c.observeRead()
	return 0
}
func (c *acceptFlagsContext) ApplyTransaction(_ tx.Transaction) (ter.Result, bool) {
	c.observeRead()
	return c.applyResult, c.applyWasSuccessful
}
func (c *acceptFlagsContext) GetParentHash() [32]byte {
	c.observeRead()
	return [32]byte{}
}
func (c *acceptFlagsContext) PreflightTransactionWithFlags(_ tx.Transaction, flags tx.ApplyFlags) ter.Result {
	c.observeRead()
	c.preflightFlags = append(c.preflightFlags, flags)
	return c.preflightResult
}
func (c *acceptFlagsContext) ApplyTransactionWithFlags(_ tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool) {
	c.observeRead()
	c.applyFlags = append(c.applyFlags, flags)
	return c.applyResult, c.applyWasSuccessful
}

type flaggedSandbox struct {
	flags   []tx.ApplyFlags
	results map[tx.Transaction]struct {
		result  ter.Result
		applied bool
	}
	commitError error
}

func (s *flaggedSandbox) ApplyTransaction(txn tx.Transaction) (ter.Result, bool) {
	entry := s.results[txn]
	return entry.result, entry.applied
}
func (s *flaggedSandbox) ApplyTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool) {
	s.flags = append(s.flags, flags)
	entry := s.results[txn]
	return entry.result, entry.applied
}
func (s *flaggedSandbox) Commit() error { return s.commitError }

type flaggedClearContext struct {
	*mockClearCtx
	sandbox SandboxContext
}

func (c *flaggedClearContext) NewSandbox() (SandboxContext, error) { return c.sandbox, nil }

func TestTxQCandidateTapUnlimitedIsUsedByAccept(t *testing.T) {
	q := mustNew(DefaultConfig())
	accountID := [20]byte{1}
	candidate := &Candidate{
		Txn:              &seqTx{seq: 1, fee: "10"},
		TxID:             [32]byte{1},
		Account:          accountID,
		FeeLevel:         FeeLevel(BaseLevel),
		SeqProxy:         NewSeqProxySequence(1),
		RetriesRemaining: RetriesAllowed,
		PreflightResult:  ter.TesSUCCESS,
		Flags:            tx.TapUNLIMITED,
		PreflightFlags:   tx.TapUNLIMITED,
	}
	aq := NewAccountQueue(accountID)
	aq.Add(candidate)
	q.byAccount[accountID] = aq
	q.byFee = append(q.byFee, candidate)
	q.byID[candidate.TxID] = candidate

	ctx := &acceptFlagsContext{
		rules:              new(amendment.Rules),
		preflightResult:    ter.TesSUCCESS,
		applyResult:        ter.TesSUCCESS,
		applyWasSuccessful: true,
	}
	ctx.readFn = func() {
		_ = q.Size()
		_ = q.AllTxs()
		_ = q.Metrics(0)
	}
	if !q.Accept(ctx) {
		t.Fatal("Accept returned false after applying candidate")
	}
	if len(ctx.applyFlags) != 1 || ctx.applyFlags[0] != tx.TapUNLIMITED {
		t.Fatalf("Accept flags = %v, want [TapUNLIMITED]", ctx.applyFlags)
	}
	if q.Size() != 0 {
		t.Fatalf("candidate remained queued after Accept: size=%d", q.Size())
	}
}

func TestTxQMixedFlagsAreUsedBySandboxClear(t *testing.T) {
	q, aq, preceding, newTx, _, seqProxy := setupClearQueue()
	preceding.Flags = tx.TapUNLIMITED
	sandbox := &flaggedSandbox{results: map[tx.Transaction]struct {
		result  ter.Result
		applied bool
	}{
		preceding.Txn: {result: ter.TesSUCCESS, applied: true},
		newTx:         {result: ter.TesSUCCESS, applied: true},
	}}
	ctx := &flaggedClearContext{
		mockClearCtx: &mockClearCtx{},
		sandbox:      sandbox,
	}
	result := q.tryClearAccountQueue(ctx, aq, newTx, seqProxy, FeeLevel(1_000_000), 4, 1, tx.TapRETRY|tx.TapUNLIMITED)
	if result == nil || !result.Applied || result.Result != ter.TesSUCCESS {
		t.Fatalf("clear result = %#v, want successful apply", result)
	}
	if len(sandbox.flags) != 2 || sandbox.flags[0] != tx.TapUNLIMITED || sandbox.flags[1] != tx.TapRETRY|tx.TapUNLIMITED {
		t.Fatalf("sandbox flags = %v, want queued TapUNLIMITED then incoming TapRETRY|TapUNLIMITED", sandbox.flags)
	}
}

func TestTxQTapRetryAndRulesChangeRerunPreflight(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	accountID := [20]byte{2}
	ctx := &flaggedAdmissionContext{
		stubApplyCtx: &stubApplyCtx{
			seq:        5,
			balance:    1_000_000_000,
			exists:     true,
			baseFee:    10,
			txInLedger: 100,
			flags:      tx.TapRETRY | tx.TapUNLIMITED,
			preflight:  ter.TesSUCCESS,
			preclaim:   ter.TesSUCCESS,
		},
		rules:           new(amendment.Rules),
		preflightResult: ter.TesSUCCESS,
		preclaimResult:  ter.TesSUCCESS,
	}
	result := q.Apply(ctx, &seqTx{seq: 5, fee: "10"}, [32]byte{2}, accountID)
	if !result.Queued || result.Result != ter.TerQUEUED {
		t.Fatalf("Apply result = %#v, want queued", result)
	}
	if len(ctx.preflightFlags) != 1 || ctx.preflightFlags[0] != tx.TapRETRY|tx.TapUNLIMITED {
		t.Fatalf("admission preflight flags = %v, want [TapRETRY|TapUNLIMITED]", ctx.preflightFlags)
	}
	candidate := q.byFee[0]
	if candidate.Flags != tx.TapUNLIMITED || candidate.PreflightFlags != tx.TapRETRY|tx.TapUNLIMITED {
		t.Fatalf("candidate flags = (%#x, %#x), want TapUNLIMITED and original preflight flags", candidate.Flags, candidate.PreflightFlags)
	}

	// A changed rules identity invalidates the cached preflight verdict. The
	// candidate's normalized flags are used for the re-check.
	accept := &acceptFlagsContext{
		rules:              new(amendment.Rules),
		preflightResult:    ter.TesSUCCESS,
		applyResult:        ter.TesSUCCESS,
		applyWasSuccessful: true,
	}
	if !q.Accept(accept) {
		t.Fatal("Accept returned false after re-preflighting candidate")
	}
	if len(accept.preflightFlags) != 1 || accept.preflightFlags[0] != tx.TapUNLIMITED {
		t.Fatalf("re-preflight flags = %v, want [TapUNLIMITED]", accept.preflightFlags)
	}
}

func TestTxQCanonicalSubmissionRejectsMismatchedIdentity(t *testing.T) {
	const accountAddress = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	txn := account.NewAccountSet(accountAddress)
	txn.Fee = "10"
	seq := uint32(1)
	txn.Sequence = &seq
	raw, err := tx.SerializeTransaction(txn)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	parsed, err := tx.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	id, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	wrongID := id
	wrongID[0]++
	if _, _, _, _, err = canonicalSubmission(txn, wrongID, [20]byte{}); err == nil {
		t.Fatal("canonicalSubmission accepted a mismatched transaction hash")
	}
	accountID := [20]byte{1}
	if _, _, _, _, err = canonicalSubmission(txn, [32]byte{}, accountID); err == nil {
		t.Fatal("canonicalSubmission accepted a mismatched transaction account")
	}
}

func TestTxQQueuedBlobAndQueryProjectionAreImmutable(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	const accountAddress = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	accountID, err := state.DecodeAccountID(accountAddress)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &stubApplyCtx{
		seq:        5,
		balance:    1_000_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 100,
		preflight:  ter.TesSUCCESS,
		preclaim:   ter.TesSUCCESS,
	}
	txn := account.NewAccountSet(accountAddress)
	txn.Fee = "10"
	seq := uint32(5)
	txn.Sequence = &seq
	raw, err := tx.SerializeTransaction(txn)
	if err != nil {
		t.Fatal(err)
	}
	txn.SetRawBytes(raw)
	txID, err := tx.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatal(err)
	}
	wantBlob := append([]byte(nil), raw...)
	if result := q.Apply(ctx, txn, txID, accountID); !result.Queued {
		t.Fatalf("Apply result = %#v, want queued", result)
	}
	raw[0] = 0xff
	blob, ok := q.GetTxBlob(txID)
	if !ok || !bytes.Equal(blob, wantBlob) {
		t.Fatalf("GetTxBlob = (%x, %v), want original copied blob", blob, ok)
	}
	details := q.AllTxs()
	if len(details) != 1 {
		t.Fatalf("query projection = %#v, want one detail", details)
	}
	details[0].TxBlob[0] = 0xee
	blob, ok = q.GetTxBlob(txID)
	if !ok || !bytes.Equal(blob, wantBlob) {
		t.Fatalf("mutating query blob changed queue storage: %x", blob)
	}
}

func TestTxQConfigRejectsZeroAndExcessiveHistory(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted zero LedgersInQueue")
	}
	if _, err := New(Config{LedgersInQueue: maxLedgersInQueue + 1}); err == nil {
		t.Fatal("New accepted excessive LedgersInQueue")
	}
	if _, err := New(DefaultConfig()); err != nil {
		t.Fatalf("New(DefaultConfig()) returned error: %v", err)
	}
}

func TestTxQConfigClampsPresentMaximumToTarget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinimumTxnInLedger = 2
	cfg.TargetTxnInLedger = 100
	cfg.MaximumTxnInLedger = 50
	cfg.MaximumTxnInLedgerSet = true
	q := mustNew(cfg)
	got := q.Config()
	if !got.MaximumTxnInLedgerSet || got.MaximumTxnInLedger != 100 {
		t.Fatalf("effective maximum = (%t, %d), want (true, 100)", got.MaximumTxnInLedgerSet, got.MaximumTxnInLedger)
	}
}

func TestTxQTapRetryRejectsTecPreclaim(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	accountID := [20]byte{10}
	ctx := &stubApplyCtx{
		seq:        1,
		balance:    1_000_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 100,
		flags:      tx.TapRETRY,
		preflight:  ter.TesSUCCESS,
		preclaim:   ter.TecCLAIM,
	}
	result := q.Apply(ctx, &seqTx{seq: 1, fee: "10"}, [32]byte{10}, accountID)
	if result.Result != ter.TecCLAIM || result.Applied || result.Queued {
		t.Fatalf("Apply result = %#v, want rejected tecCLAIM", result)
	}
	if q.Size() != 0 {
		t.Fatalf("queue size = %d, want 0", q.Size())
	}
}

func TestTxQCapacityAndFillArithmeticDoNotOverflow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinimumTxnInLedger = math.MaxUint32
	cfg.TargetTxnInLedger = math.MaxUint32
	capacityQueue := mustNew(cfg)
	capacityQueue.ProcessClosedLedger(&stubClosedLedgerCtx{}, false)
	wantCapacity := uint64(math.MaxUint32) * uint64(cfg.LedgersInQueue)
	if capacityQueue.maxSize == nil || *capacityQueue.maxSize != wantCapacity {
		t.Fatalf("maxSize = %v, want %d", capacityQueue.maxSize, wantCapacity)
	}

	q := mustNew(DefaultConfig())
	q.setMaxSize(math.MaxUint64)
	q.byFee = append(q.byFee, &Candidate{})
	if q.isFullPct(95) {
		t.Fatal("max-sized queue reported full with one transaction")
	}
	if !q.isFullPct(0) {
		t.Fatal("zero-percent fill must report full")
	}
	q.setMaxSize(101)
	q.byFee = make([]*candidate, 95)
	if !q.isFullPct(95) {
		t.Fatal("95 of 101 entries did not meet the truncated 95% threshold")
	}
}

func TestTxQCommitFailureDoesNotMutateRetryState(t *testing.T) {
	q, aq, preceding, newTx, _, seqProxy := setupClearQueue()
	preceding.RetriesRemaining = 7
	preceding.LastResult = ter.TemMALFORMED
	sb := &mockSandbox{
		results: mkResults(
			struct {
				tx      *mockTx
				res     ter.Result
				applied bool
			}{preceding.Txn.(*mockTx), ter.TesSUCCESS, true},
			struct {
				tx      *mockTx
				res     ter.Result
				applied bool
			}{newTx, ter.TesSUCCESS, true},
		),
		commitErr: errors.New("commit failed"),
	}
	ctx := &mockClearCtx{sandbox: sb}
	result := q.tryClearAccountQueue(ctx, aq, newTx, seqProxy, FeeLevel(1_000_000), 4, 1)
	if result == nil || result.Result != ter.TefINTERNAL || result.Queued {
		t.Fatalf("commit failure result = %#v, want non-queued tefINTERNAL", result)
	}
	if preceding.RetriesRemaining != 7 || preceding.LastResult != ter.TemMALFORMED {
		t.Fatalf("retry state changed after commit failure: retries=%d result=%v", preceding.RetriesRemaining, preceding.LastResult)
	}
	if _, ok := aq.Transactions[preceding.SeqProxy]; !ok || len(q.byFee) != 1 {
		t.Fatal("queue changed after commit failure")
	}
}

func TestTxQAcceptHealsOrphanedIndexes(t *testing.T) {
	q := mustNew(DefaultConfig())
	accountID := [20]byte{4}
	candidate := &Candidate{
		Txn:              &seqTx{seq: 1, fee: "10"},
		TxID:             [32]byte{4},
		Account:          accountID,
		FeeLevel:         FeeLevel(BaseLevel),
		SeqProxy:         NewSeqProxySequence(1),
		RetriesRemaining: RetriesAllowed,
	}
	q.byFee = append(q.byFee, candidate)
	q.byID[candidate.TxID] = candidate
	if q.Accept(&acceptFlagsContext{}) {
		t.Fatal("Accept reported a ledger change for orphaned candidate")
	}
	if q.Size() != 0 {
		t.Fatalf("orphan remained in byFee: size=%d", q.Size())
	}
	if _, ok := q.byID[candidate.TxID]; ok {
		t.Fatal("orphan remained in byID")
	}
}

func TestTxQReadCallbacksCanReenterApply(t *testing.T) {
	q := mustNew(DefaultConfig())
	accountID := [20]byte{5}
	ctx := &stubApplyCtx{
		seq:       1,
		balance:   1_000_000_000,
		exists:    true,
		baseFee:   10,
		preflight: ter.TesSUCCESS,
		applyRes:  ter.TesSUCCESS,
		applied:   true,
	}
	ctx.applyFn = func(tx.Transaction) (ter.Result, bool) {
		_ = q.Size()
		_ = q.AllTxs()
		_ = q.Metrics(0)
		return ter.TesSUCCESS, true
	}
	done := make(chan ApplyResult, 1)
	go func() { done <- q.Apply(ctx, &seqTx{seq: 1, fee: "10"}, [32]byte{5}, accountID) }()
	select {
	case result := <-done:
		if !result.Applied {
			t.Fatalf("reentrant Apply result = %#v, want applied", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Apply deadlocked while callback queried queue snapshots")
	}
}

func TestTxQAdmissionCallbacksCanReenterQueueReads(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	accountID := [20]byte{8}
	ctx := &stubApplyCtx{
		seq:        1,
		balance:    1_000_000_000,
		reserve:    200,
		exists:     true,
		baseFee:    10,
		txInLedger: 100,
		preflight:  ter.TesSUCCESS,
		preclaim:   ter.TesSUCCESS,
	}
	ctx.readFn = func() {
		_ = q.Size()
		_ = q.AllTxs()
		_ = q.Metrics(0)
	}
	done := make(chan []ApplyResult, 1)
	go func() {
		done <- []ApplyResult{
			q.Apply(ctx, &seqTx{seq: 1, fee: "10"}, [32]byte{8}, accountID),
			q.Apply(ctx, &seqTx{seq: 2, fee: "10"}, [32]byte{9}, accountID),
		}
	}()
	select {
	case results := <-done:
		for i, result := range results {
			if !result.Queued {
				t.Fatalf("submission %d result = %#v, want queued", i, result)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Apply deadlocked while an admission callback queried queue snapshots")
	}
}

func TestTxQConcurrentSnapshotsRemainConsistent(t *testing.T) {
	q := mustNew(DefaultConfig())
	accountID := [20]byte{6}
	q.byAccount[accountID] = NewAccountQueue(accountID)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = q.Size()
				_ = q.AllTxs()
				_ = q.AccountTxs(accountID)
				_ = q.Metrics(uint32(j))
			}
		}()
	}
	for i := 0; i < 100; i++ {
		q.setMaxSize(uint64(i + 1))
	}
	wg.Wait()
}

func TestTxQMissingBlockerTypesSetAuthChange(t *testing.T) {
	transactions := []tx.Transaction{
		&account.AccountDelete{BaseTx: *tx.NewBaseTx(tx.TypeAccountDelete, "rTest")},
		&xchain.XChainClaim{BaseTx: *tx.NewBaseTx(tx.TypeXChainClaim, "rTest")},
		&xchain.XChainAddClaimAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddClaimAttestation, "rTest")},
		&xchain.XChainAddAccountCreateAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddAccountCreateAttest, "rTest")},
	}
	for _, txn := range transactions {
		txn.GetCommon().Fee = "10"
		if !computeConsequences(txn, NewSeqProxySequence(1)).IsBlocker {
			t.Errorf("computeConsequences(%s) did not mark blocker", txn.TxType())
		}
	}
}

func TestTxQQueryProjectionPreservesConsequences(t *testing.T) {
	q := mustNew(DefaultConfig())
	accountID := [20]byte{7}
	candidate := &Candidate{
		Txn:              &seqTx{seq: 1, fee: "10"},
		TxID:             [32]byte{7},
		Account:          accountID,
		FeeLevel:         FeeLevel(BaseLevel),
		SeqProxy:         NewSeqProxySequence(1),
		Consequences:     TxConsequences{Fee: 10, PotentialSpend: 99, IsBlocker: true},
		RetriesRemaining: RetriesAllowed,
		blob:             []byte{1, 2, 3},
	}
	aq := NewAccountQueue(accountID)
	aq.Add(candidate)
	q.byAccount[accountID] = aq
	q.byFee = append(q.byFee, candidate)
	q.byID[candidate.TxID] = candidate
	details := q.AllTxs()
	if len(details) != 1 {
		t.Fatalf("AllTxs returned %d details, want one", len(details))
	}
	if details[0].Fee != 10 || details[0].PotentialSpend != 99 || !details[0].AuthChange {
		t.Fatalf("query consequences = %#v, want fee/spend/auth-change preserved", details[0])
	}
}

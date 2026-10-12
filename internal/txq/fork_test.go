package txq

import (
	"bytes"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const forkAccountAddress = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func TestTxQForkCopiesQueueStateAndAliases(t *testing.T) {
	q, accountID := newForkQueue(t)
	originalMetrics := q.Metrics(0)
	originalCandidate := q.byFee[0]
	originalBlob := bytes.Clone(originalCandidate.blob)
	originalRetries := originalCandidate.RetriesRemaining
	originalMaxSize := *q.maxSize
	originalFeeHistory := slices.Clone(q.feeMetrics.recentTxnCounts)

	fork, err := q.Fork()
	if err != nil {
		t.Fatalf("Fork returned error: %v", err)
	}

	forkCandidate := fork.byID[originalCandidate.TxID]
	if forkCandidate == nil || forkCandidate == originalCandidate {
		t.Fatal("fork did not clone candidate")
	}
	if fork.byFee[0] != forkCandidate {
		t.Fatal("fork byFee index does not point to the cloned candidate")
	}
	if fork.byAccount[accountID].Transactions[forkCandidate.SeqProxy] != forkCandidate {
		t.Fatal("fork account index does not point to the cloned candidate")
	}
	if reflect.ValueOf(forkCandidate.Txn).Pointer() == reflect.ValueOf(originalCandidate.Txn).Pointer() {
		t.Fatal("fork shares the parsed transaction object")
	}
	if !bytes.Equal(forkCandidate.blob, originalBlob) {
		t.Fatal("fork changed the candidate blob while copying")
	}
	if fork.maxSize == q.maxSize {
		t.Fatal("fork shares maxSize storage")
	}
	if fork.feeMetrics == q.feeMetrics {
		t.Fatal("fork shares fee metrics")
	}
	if len(fork.feeMetrics.recentTxnCounts) == 0 || &fork.feeMetrics.recentTxnCounts[0] == &q.feeMetrics.recentTxnCounts[0] {
		t.Fatal("fork shares fee metric history storage")
	}

	forkCandidate.RetriesRemaining = 1
	forkCandidate.blob[0] ^= 0xff
	forkRaw := forkCandidate.Txn.GetRawBytes()
	forkRaw[0] ^= 0xff
	forkCandidate.Txn.SetRawBytes(forkRaw)
	fork.byAccount[accountID].RetryPenalty = false
	*fork.maxSize = 1
	fork.feeMetrics.recentTxnCounts[0]++
	if forkCandidate.RetriesRemaining == originalRetries {
		t.Fatal("fork candidate mutation was not applied")
	}

	if originalCandidate.RetriesRemaining != originalRetries {
		t.Fatalf("source retries changed through fork: got %d, want %d", originalCandidate.RetriesRemaining, originalRetries)
	}
	if !bytes.Equal(originalCandidate.blob, originalBlob) {
		t.Fatal("source blob changed through fork mutation")
	}
	if !bytes.Equal(originalCandidate.Txn.GetRawBytes(), originalBlob) {
		t.Fatal("source parsed transaction changed through fork mutation")
	}
	if !q.byAccount[accountID].RetryPenalty {
		t.Fatal("source account penalty changed through fork mutation")
	}
	if *q.maxSize != originalMaxSize {
		t.Fatal("source maximum size changed through fork mutation")
	}
	if !slices.Equal(q.feeMetrics.recentTxnCounts, originalFeeHistory) {
		t.Fatal("source fee history changed through fork mutation")
	}
	if got := q.Metrics(0); !reflect.DeepEqual(got, originalMetrics) {
		t.Fatalf("source metrics changed through fork mutation: got %#v, want %#v", got, originalMetrics)
	}
}

func TestTxQForkRejectsMalformedSyntheticCandidate(t *testing.T) {
	q := mustNew(DefaultConfig())
	accountID := [20]byte{1}
	accountQueue := newAccountQueue(accountID)
	sourceTxn := &seqTx{seq: 1, fee: "10"}
	candidate := &candidate{
		Txn:              sourceTxn,
		blob:             []byte{0x01},
		TxID:             [32]byte{1},
		Account:          accountID,
		FeeLevel:         FeeLevel(BaseLevel),
		SeqProxy:         NewSeqProxySequence(1),
		RetriesRemaining: RetriesAllowed,
	}
	accountQueue.Add(candidate)
	q.byAccount[accountID] = accountQueue
	q.insertByFee(candidate)
	originalBlob := bytes.Clone(candidate.blob)

	fork, err := q.Fork()
	if err == nil || fork != nil {
		t.Fatalf("Fork = (%p, %v), want no fork and a malformed-transaction error", fork, err)
	}
	if q.Size() != 1 || q.byFee[0] != candidate || q.byID[candidate.TxID] != candidate {
		t.Fatal("source indexes changed after rejected fork")
	}
	if !bytes.Equal(candidate.blob, originalBlob) || candidate.Txn != sourceTxn {
		t.Fatal("source candidate changed after rejected fork")
	}
}

func TestTxQForkDiscardDoesNotMutateSource(t *testing.T) {
	q, _ := newForkQueue(t)
	originalSize := q.Size()
	original := q.AllTxs()
	originalPenalties := make(map[[20]byte][2]bool, len(q.byAccount))
	for accountID, queue := range q.byAccount {
		originalPenalties[accountID] = [2]bool{queue.RetryPenalty, queue.DropPenalty}
	}

	fork, err := q.Fork()
	if err != nil {
		t.Fatalf("Fork returned error: %v", err)
	}
	if fork.Accept(&stubApplyCtx{applyRes: ter.TefFAILURE}) {
		t.Fatal("fork Accept reported a ledger change for rejected transactions")
	}
	if fork.Size() != 0 {
		t.Fatalf("fork retained permanently rejected transactions: size=%d", fork.Size())
	}
	if q.Size() != originalSize {
		t.Fatalf("source size changed after fork discard: got %d, want %d", q.Size(), originalSize)
	}
	if got := q.AllTxs(); !reflect.DeepEqual(got, original) {
		t.Fatalf("source candidates changed after fork discard: got %#v, want %#v", got, original)
	}
	for accountID, want := range originalPenalties {
		queue := q.byAccount[accountID]
		got := [2]bool{queue.RetryPenalty, queue.DropPenalty}
		if got != want {
			t.Fatalf("source penalties changed for %x: got %v, want %v", accountID, got, want)
		}
	}
}

func TestTxQForkMetricsAndIndexesAreIndependent(t *testing.T) {
	q, _ := newForkQueue(t)
	sourceBefore := q.Metrics(0)
	sourceSnapshot := q.feeMetrics.snapshot()

	fork, err := q.Fork()
	if err != nil {
		t.Fatalf("Fork returned error: %v", err)
	}
	fork.ProcessClosedLedger(&forkClosedLedgerContext{
		sequence: 1,
		count:    300,
		fees:     []FeeLevel{FeeLevel(BaseLevel * 2), FeeLevel(BaseLevel * 3), FeeLevel(BaseLevel * 4)},
	}, false)

	if got := q.Metrics(0); !reflect.DeepEqual(got, sourceBefore) {
		t.Fatalf("source metrics changed after fork processing: got %#v, want %#v", got, sourceBefore)
	}
	if got := q.feeMetrics.snapshot(); got != sourceSnapshot {
		t.Fatalf("source fee snapshot changed after fork processing: got %#v, want %#v", got, sourceSnapshot)
	}
	if reflect.DeepEqual(fork.Metrics(0), sourceBefore) {
		t.Fatal("fork processing did not update fork metrics")
	}
	if fork.maxSize == nil || q.maxSize == nil || *fork.maxSize == *q.maxSize {
		t.Fatal("fork processing did not maintain an independent dynamic maximum")
	}
	if len(fork.byFee) != len(fork.byID) {
		t.Fatalf("fork indexes disagree after processing: byFee=%d byID=%d", len(fork.byFee), len(fork.byID))
	}
}

func TestTxQForkParsedTransactionsDoNotRaceWithSourceReads(t *testing.T) {
	q, _ := newForkQueue(t)
	fork, err := q.Fork()
	if err != nil {
		t.Fatalf("Fork returned error: %v", err)
	}
	sourceTxn := q.byFee[0].Txn
	forkTxn := fork.byFee[0].Txn
	if reflect.ValueOf(sourceTxn).Pointer() == reflect.ValueOf(forkTxn).Pointer() {
		t.Fatal("fork shares parsed transaction storage")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			q.stateMu.RLock()
			_ = sourceTxn.GetRawBytes()
			q.stateMu.RUnlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			fork.stateMu.Lock()
			forkTxn.SetRawBytes([]byte{byte(i)})
			fork.stateMu.Unlock()
		}
	}()
	wg.Wait()
}

type forkClosedLedgerContext struct {
	sequence uint32
	count    uint32
	fees     []FeeLevel
}

func (c *forkClosedLedgerContext) GetLedgerSequence() uint32           { return c.sequence }
func (c *forkClosedLedgerContext) GetTransactionCount() uint32         { return c.count }
func (c *forkClosedLedgerContext) GetTransactionFeeLevels() []FeeLevel { return c.fees }

func newForkQueue(t *testing.T) (*TxQ, [20]byte) {
	t.Helper()
	q := mustNew(DefaultConfig())
	q.parentHash = [32]byte{0x42}
	q.txqFull = 7
	maxSize := uint64(200)
	q.maxSize = &maxSize
	q.feeMetrics.update([]FeeLevel{FeeLevel(BaseLevel * 2), FeeLevel(BaseLevel * 3), FeeLevel(BaseLevel * 4)}, 300, false, q.config)

	accountID, err := state.DecodeAccountID(forkAccountAddress)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	accountQueue := newAccountQueue(accountID)
	accountQueue.RetryPenalty = true
	accountQueue.DropPenalty = true
	q.byAccount[accountID] = accountQueue

	for seq, fee := range []FeeLevel{FeeLevel(BaseLevel * 3), FeeLevel(BaseLevel * 2)} {
		candidate := newForkCandidate(t, accountID, uint32(seq+1), fee)
		candidate.RetriesRemaining = RetriesAllowed - seq
		candidate.LastResult = ter.TelCAN_NOT_QUEUE
		accountQueue.Add(candidate)
		q.insertByFee(candidate)
	}
	return q, accountID
}

func newForkCandidate(t *testing.T, accountID [20]byte, sequence uint32, feeLevel FeeLevel) *candidate {
	t.Helper()
	txn := account.NewAccountSet(forkAccountAddress)
	txn.Fee = "10"
	txn.Sequence = &sequence
	raw, err := tx.SerializeTransaction(txn)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	parsed, err := tx.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	txID, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	return &candidate{
		Txn:              parsed,
		blob:             bytes.Clone(raw),
		TxID:             txID,
		Account:          accountID,
		FeeLevel:         feeLevel,
		SeqProxy:         NewSeqProxySequence(sequence),
		LastValid:        100,
		RetriesRemaining: RetriesAllowed,
		PreflightResult:  ter.TesSUCCESS,
		Consequences: txConsequences{
			Fee:          10,
			FollowingSeq: NewSeqProxySequence(sequence + 1),
		},
	}
}

package archive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// fakeRepo captures SaveBatch calls in-memory. Zero config of its own so
// tests can wrap it with a latency knob when they need one.
type fakeRepo struct {
	mu       sync.Mutex
	rows     []*relationaldb.ValidationRecord
	batches  int
	saveWait time.Duration

	deletes   []int64 // maxSeq arguments in order
	deleteErr error
	deleteCh  chan int64
}

func (f *fakeRepo) Save(ctx context.Context, v *relationaldb.ValidationRecord) error {
	return f.SaveBatch(ctx, []*relationaldb.ValidationRecord{v})
}

func (f *fakeRepo) SaveBatch(ctx context.Context, vs []*relationaldb.ValidationRecord) error {
	if f.saveWait > 0 {
		timer := time.NewTimer(f.saveWait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, vs...)
	f.batches++
	return nil
}

func (f *fakeRepo) GetValidationsForLedger(ctx context.Context, seq relationaldb.LedgerIndex) ([]*relationaldb.ValidationRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*relationaldb.ValidationRecord
	for _, r := range f.rows {
		if r.LedgerSeq == seq {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRepo) GetValidationsByValidator(ctx context.Context, nodeKey []byte, limit int) ([]*relationaldb.ValidationRecord, error) {
	return nil, nil
}

func (f *fakeRepo) GetValidationCount(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.rows)), nil
}

func (f *fakeRepo) DeleteOlderThanSeq(ctx context.Context, maxSeq relationaldb.LedgerIndex, batchSize int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, int64(maxSeq))
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	kept := f.rows[:0]
	removed := int64(0)
	for _, r := range f.rows {
		if r.LedgerSeq < maxSeq && (batchSize <= 0 || removed < int64(batchSize)) {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	f.rows = kept
	if f.deleteCh != nil {
		f.deleteCh <- removed
	}
	return removed, nil
}

func (f *fakeRepo) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func mkVal(seq uint32, node byte) *consensus.Validation {
	v := &consensus.Validation{
		LedgerSeq: seq,
		Full:      true,
		SignTime:  time.Unix(1700000000, 0).UTC(),
		SeenTime:  time.Unix(1700000001, 0).UTC(),
		Signature: []byte{0xAB, 0xCD},
		Raw:       []byte{0xFE, 0xED, byte(seq), node},
	}
	v.LedgerID[0] = byte(seq)
	v.LedgerID[31] = node
	v.SigningPubKey[0] = 0x02
	v.SigningPubKey[32] = node
	v.NodeID = consensus.CalcNodeID([33]byte(v.SigningPubKey))
	return v
}

func TestArchive_BatchesOnSize(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 3, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	for i := uint32(1); i <= 3; i++ {
		a.OnStale(mkVal(i, 1))
	}

	// Size-triggered commit must land without waiting for the tick.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.rowCount() == 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected 3 rows, got %d", repo.rowCount())
}

func TestArchive_BatchesOnTick(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1000, FlushInterval: 30 * time.Millisecond, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	for i := uint32(1); i <= 5; i++ {
		a.OnStale(mkVal(i, 1))
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.rowCount() == 5 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected 5 rows via tick flush, got %d", repo.rowCount())
}

func TestArchive_Flush_Drains(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1000, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	for i := uint32(1); i <= 7; i++ {
		a.OnStale(mkVal(i, 1))
	}
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.rowCount() != 7 {
		t.Fatalf("Flush did not drain: got %d rows, want 7", repo.rowCount())
	}
}

func TestArchive_CloseDrainsPending(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1000, FlushInterval: time.Hour, DeleteBatch: 1}, nil)

	for i := uint32(1); i <= 4; i++ {
		a.OnStale(mkVal(i, 1))
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.rowCount() != 4 {
		t.Fatalf("Close did not commit pending rows: got %d, want 4", repo.rowCount())
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Close(canceled); err != nil {
		t.Fatalf("second Close errored: %v", err)
	}
}

func TestArchive_OnStale_NonBlocking_UnderSlowRepo(t *testing.T) {
	repo := &fakeRepo{saveWait: 20 * time.Millisecond}
	a := New(repo, Config{BatchSize: 8, FlushInterval: 5 * time.Millisecond, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	// BatchSize=8 → channel buffer=64. Fire 32 quickly; all should be
	// accepted via the fast path without blocking.
	start := time.Now()
	for i := uint32(1); i <= 32; i++ {
		a.OnStale(mkVal(i, 1))
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("OnStale loop blocked on slow repo: took %v", elapsed)
	}
}

func TestArchive_ApplyRetention_HonorsLastSeq(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1000, FlushInterval: time.Hour, RetentionLedgers: 10, DeleteBatch: 1000}, nil)
	defer a.Close(context.Background())

	for i := uint32(1); i <= 20; i++ {
		a.OnStale(mkVal(i, byte(i)))
	}
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.rowCount() != 20 {
		t.Fatalf("pre-retention rowCount=%d, want 20", repo.rowCount())
	}

	a.NoteFullyValidated(20)
	if _, err := a.ApplyRetention(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Rows with LedgerSeq < (20 - 10) = 10 should be gone → seqs 1..9.
	if repo.rowCount() != 11 {
		t.Fatalf("post-retention rowCount=%d, want 11 (seqs 10..20)", repo.rowCount())
	}
}

func TestArchive_ApplyRetention_ZeroRetention_Noop(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1000, FlushInterval: time.Hour, RetentionLedgers: 0, DeleteBatch: 1000}, nil)
	defer a.Close(context.Background())

	for i := uint32(1); i <= 5; i++ {
		a.OnStale(mkVal(i, byte(i)))
	}
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.NoteFullyValidated(5)

	n, err := a.ApplyRetention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("zero retention must be a no-op; got %d deletions", n)
	}
	if repo.rowCount() != 5 {
		t.Fatalf("zero retention must keep everything; got %d rows, want 5", repo.rowCount())
	}
}

func TestArchive_NoteFullyValidated_MonotonicallyIncreases(t *testing.T) {
	a := &Archive{}

	a.NoteFullyValidated(100)
	a.NoteFullyValidated(50) // older update must be ignored
	a.NoteFullyValidated(120)

	if got := a.lastSeq.Load(); got != 120 {
		t.Fatalf("lastSeq=%d, want 120", got)
	}
}

func TestArchive_NilRepo_OnStaleIsNoop(t *testing.T) {
	a := New(nil, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	// Must not panic, must not block.
	for i := uint32(1); i <= 10; i++ {
		a.OnStale(mkVal(i, 1))
	}
}

// flakyRepo wraps fakeRepo and returns an error from SaveBatch a
// configurable number of times before succeeding. Exercises the
// retry-once policy in the writer loop.
type flakyRepo struct {
	*fakeRepo
	mu          sync.Mutex
	failures    int
	failureLeft int
}

func (f *flakyRepo) SaveBatch(ctx context.Context, vs []*relationaldb.ValidationRecord) error {
	f.mu.Lock()
	if f.failureLeft > 0 {
		f.failureLeft--
		f.failures++
		f.mu.Unlock()
		return errors.New("transient repo failure")
	}
	f.mu.Unlock()
	return f.fakeRepo.SaveBatch(ctx, vs)
}

func TestArchive_SaveBatch_RetryOnceThenSucceed(t *testing.T) {
	base := &fakeRepo{}
	repo := &flakyRepo{fakeRepo: base, failureLeft: 1} // first attempt fails, retry succeeds
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	a.OnStale(mkVal(100, 0x01))
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	if base.rowCount() != 1 {
		t.Fatalf("retry path lost the row: rowCount=%d, want 1", base.rowCount())
	}
	if repo.failures != 1 {
		t.Fatalf("expected exactly 1 failed attempt before success, got %d", repo.failures)
	}
}

func TestArchive_SaveBatch_PersistentFailureRemainsObservable(t *testing.T) {
	base := &fakeRepo{}
	// More failures than retries → batch is dropped after attempts.
	repo := &flakyRepo{fakeRepo: base, failureLeft: 100}
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)

	a.OnStale(mkVal(100, 0x01))
	if err := a.Flush(context.Background()); !errors.Is(err, ErrDurability) {
		t.Fatalf("Flush returned %v, want ErrDurability", err)
	}

	// Failed batch must be dropped — no row in the underlying repo.
	if base.rowCount() != 0 {
		t.Fatalf("permanently failing batch was not dropped: rowCount=%d", base.rowCount())
	}

	// Writer must still be alive: a follow-up validation under a
	// recovered repo should land. Reset the failure counter.
	repo.mu.Lock()
	repo.failureLeft = 0
	repo.mu.Unlock()

	a.OnStale(mkVal(101, 0x02))
	if err := a.Flush(context.Background()); !errors.Is(err, ErrDurability) {
		t.Fatalf("later Flush returned %v, want sticky ErrDurability", err)
	}
	if base.rowCount() != 1 {
		t.Fatalf("writer dead after persistent failure: rowCount=%d, want 1", base.rowCount())
	}
	if err := a.Close(context.Background()); !errors.Is(err, ErrDurability) {
		t.Fatalf("Close returned %v, want sticky ErrDurability", err)
	}
	health := a.Health()
	if health.WriteFailures != 1 || health.PersistenceDropped != 1 || health.Healthy {
		t.Fatalf("unexpected health after persistence failure: %+v", health)
	}
}

func TestArchive_FlushAfterClose_ReturnsErrClosed(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)

	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Flush(context.Background()); err != ErrClosed {
		t.Fatalf("Flush after Close returned %v, want ErrClosed", err)
	}
}

func TestArchive_OnStale_AfterClose_IsNoop(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)

	_ = a.Close(context.Background())

	var dropped atomic.Int32
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a.OnStale(mkVal(uint32(i+1), 1))
			dropped.Add(1)
		}(i)
	}
	// A bounded wait is enough — OnStale must never block after Close.
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("OnStale blocked after Close; completed %d/50", dropped.Load())
	}
	if got := a.Health().ClosedDropped; got != 50 {
		t.Fatalf("closed drop count=%d, want 50", got)
	}
}

type blockingRepo struct {
	*fakeRepo
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRepo() *blockingRepo {
	return &blockingRepo{
		fakeRepo: &fakeRepo{},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (r *blockingRepo) SaveBatch(
	ctx context.Context,
	vs []*relationaldb.ValidationRecord,
) error {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return r.fakeRepo.SaveBatch(ctx, vs)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestArchive_FlushRacingCloseWaitsForPersistence(t *testing.T) {
	repo := newBlockingRepo()
	a := New(repo, Config{BatchSize: 1000, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	a.OnStale(mkVal(1, 1))

	flushDone := make(chan error, 1)
	go func() { flushDone <- a.Flush(context.Background()) }()
	<-repo.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- a.Close(context.Background()) }()

	select {
	case err := <-flushDone:
		t.Fatalf("Flush returned before persistence completed: %v", err)
	default:
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before persistence completed: %v", err)
	default:
	}

	close(repo.release)
	if err := <-flushDone; err != nil {
		t.Fatalf("Flush returned %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close returned %v", err)
	}
	if got := repo.rowCount(); got != 1 {
		t.Fatalf("persisted rows=%d, want 1", got)
	}
}

func TestArchive_FlushRacingCloseReturnsErrClosedWhenCloseWins(t *testing.T) {
	repo := newBlockingRepo()
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	a.OnStale(mkVal(1, 1))
	<-repo.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- a.Close(context.Background()) }()
	<-a.stop

	if err := a.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush returned %v, want ErrClosed", err)
	}
	close(repo.release)
	if err := <-closeDone; err != nil {
		t.Fatalf("Close returned %v", err)
	}
}

func TestArchive_CloseTimeoutRemainsWaitable(t *testing.T) {
	repo := newBlockingRepo()
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	a.OnStale(mkVal(1, 1))
	<-repo.started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Close returned %v, want deadline exceeded", err)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	err := a.Close(waitCtx)
	if !errors.Is(err, ErrDurability) {
		t.Fatalf("second Close returned %v, want terminal ErrDurability", err)
	}
	if got := a.Health().PersistenceDropped; got != 1 {
		t.Fatalf("persistence drops=%d, want 1", got)
	}
}

func TestArchive_OnStaleSaturationIsNonBlockingAndCounted(t *testing.T) {
	repo := newBlockingRepo()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a := New(repo, Config{BatchSize: 1, FlushInterval: time.Hour, DeleteBatch: 1}, logger)

	a.OnStale(mkVal(1, 1))
	<-repo.started
	for i := range cap(a.ch) {
		a.OnStale(mkVal(uint32(i+2), 1))
	}
	start := time.Now()
	for i := range 100 {
		a.OnStale(mkVal(uint32(i+100), 2))
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("100 saturated OnStale calls took %v", elapsed)
	}

	health := a.Health()
	if health.OverloadDropped != 100 {
		t.Fatalf("overload drops=%d, want 100", health.OverloadDropped)
	}
	if got := strings.Count(logs.String(), "channel full"); got != 1 {
		t.Fatalf("overload warning count=%d, want 1\n%s", got, logs.String())
	}

	close(repo.release)
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := repo.rowCount(), int(health.Enqueued); got != want {
		t.Fatalf("persisted rows=%d, admitted=%d", got, want)
	}
}

func TestArchive_ConcurrentAdmissionAndCloseAccountsEveryValidation(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 1000, FlushInterval: time.Hour, DeleteBatch: 1}, nil)

	start := make(chan struct{})
	var producers sync.WaitGroup
	for i := range 200 {
		producers.Add(1)
		go func(i int) {
			defer producers.Done()
			<-start
			a.OnStale(mkVal(uint32(i+1), byte(i)))
		}(i)
	}
	closeDone := make(chan error, 1)
	go func() {
		<-start
		closeDone <- a.Close(context.Background())
	}()
	close(start)
	producers.Wait()
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}

	health := a.Health()
	if got := health.Enqueued + health.ClosedDropped + health.OverloadDropped; got != 200 {
		t.Fatalf("accounted validations=%d, want 200; health=%+v", got, health)
	}
	if got := uint64(repo.rowCount()); got != health.Enqueued {
		t.Fatalf("persisted rows=%d, admitted=%d", got, health.Enqueued)
	}
	if got := len(a.ch); got != 0 {
		t.Fatalf("buffered rows after Close=%d, want 0", got)
	}
}

func TestArchive_MalformedDropsAreCountedAndRateLimited(t *testing.T) {
	repo := &fakeRepo{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a := New(repo, Config{BatchSize: 100, FlushInterval: time.Hour, DeleteBatch: 1}, logger)
	defer a.Close(context.Background())

	for i := range 5 {
		v := mkVal(uint32(i+1), 1)
		v.Raw = nil
		a.OnStale(v)
	}
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := a.Health()
	if health.MalformedDropped != 5 {
		t.Fatalf("malformed drops=%d, want 5", health.MalformedDropped)
	}
	if got := strings.Count(logs.String(), "without canonical raw bytes"); got != 1 {
		t.Fatalf("malformed warning count=%d, want 1\n%s", got, logs.String())
	}
}

func TestArchive_PersistsPartialValidation(t *testing.T) {
	repo := &fakeRepo{}
	a := New(repo, Config{BatchSize: 100, FlushInterval: time.Hour, DeleteBatch: 1}, nil)
	defer a.Close(context.Background())

	v := mkVal(10, 1)
	v.Full = false
	v.Flags = 0x80000000
	a.OnStale(v)
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := repo.rowCount(); got != 1 {
		t.Fatalf("partial validation rows=%d, want 1", got)
	}
}

func TestArchive_RetentionRunsIdleAndContinuesPastBudget(t *testing.T) {
	deleteCh := make(chan int64, 32)
	repo := &fakeRepo{deleteCh: deleteCh}
	for seq := uint32(1); seq <= 30; seq++ {
		repo.rows = append(repo.rows, toRecord(mkVal(seq, byte(seq)), seq))
	}
	ticks := make(chan time.Time, 1)
	a := newArchive(
		repo,
		Config{
			BatchSize:        100,
			FlushInterval:    time.Hour,
			RetentionLedgers: 10,
			DeleteBatch:      2,
		},
		nil,
		ticks,
	)
	defer a.Close(context.Background())
	a.NoteFullyValidated(30)

	ticks <- time.Now()
	for i := 0; i < 10; i++ {
		<-deleteCh
	}
	if got := repo.rowCount(); got != 11 {
		t.Fatalf("rows after idle catch-up=%d, want 11", got)
	}
	if health := a.Health(); health.RetentionFailures != 0 {
		t.Fatalf("unexpected retention health: %+v", health)
	}
}

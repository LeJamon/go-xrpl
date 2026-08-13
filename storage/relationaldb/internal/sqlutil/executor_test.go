package sqlutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

type blockingValidationRepository struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingValidationRepository) Save(context.Context, *relationaldb.ValidationRecord) error {
	return nil
}

func (r *blockingValidationRepository) SaveBatch(context.Context, []*relationaldb.ValidationRecord) error {
	close(r.started)
	<-r.release
	return nil
}

func (r *blockingValidationRepository) GetValidationsForLedger(
	context.Context,
	relationaldb.LedgerIndex,
) ([]*relationaldb.ValidationRecord, error) {
	return nil, nil
}

func (r *blockingValidationRepository) GetValidationsByValidator(
	context.Context,
	[]byte,
	int,
) ([]*relationaldb.ValidationRecord, error) {
	return nil, nil
}

func (r *blockingValidationRepository) DeleteOlderThanSeq(
	context.Context,
	relationaldb.LedgerIndex,
	int,
) (int64, error) {
	return 0, nil
}

func TestGatedValidationRepositoryPinsBatchUntilClose(t *testing.T) {
	var gate OperationGate
	backend := &blockingValidationRepository{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	repository := NewGatedValidationRepository(&gate, backend)
	batchDone := make(chan error, 1)
	go func() {
		batchDone <- repository.SaveBatch(context.Background(), nil)
	}()
	<-backend.started

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- gate.Close(func() error { return nil })
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !gate.closing.Load() {
		select {
		case <-deadline.C:
			t.Fatal("Close did not enter closing state")
		case <-ticker.C:
		}
	}
	if err := repository.Save(context.Background(), nil); !errors.Is(err, relationaldb.ErrDatabaseClosed) {
		t.Fatalf("Save while Close was pending returned %v, want %v", err, relationaldb.ErrDatabaseClosed)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before SaveBatch completed: %v", err)
	default:
	}
	close(backend.release)
	if err := <-batchDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

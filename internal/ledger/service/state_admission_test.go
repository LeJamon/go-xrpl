package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type admissionMissingFamily struct {
	shamap.Family
	root [32]byte
}

func (f admissionMissingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash != f.root {
		return nil, nil
	}
	return f.Family.Fetch(ctx, hash)
}

func TestStateAdmissionRejectsMissingDetachedChild(t *testing.T) {
	base := backend.NewMemory()
	source := shamap.New(shamap.TypeState)
	require.NoError(t, source.Put([32]byte{1}, []byte("state-value-1234")))
	require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
		return base.StoreBatch(t.Context(), entries)
	}))
	root, err := source.Hash()
	require.NoError(t, err)
	family := admissionMissingFamily{Family: base, root: root}
	cold, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, root, family)
	require.NoError(t, err)

	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	releaseAdmission, err := svc.AcquireStateAdmission(t.Context())
	require.NoError(t, err)
	defer releaseAdmission()

	err = svc.VerifyDetachedMaps(t.Context(), cold, nil)
	require.ErrorIs(t, err, shamap.ErrNodeNotInStore)
	require.Regexp(t, regexp.MustCompile(`[0-9a-f]{64}`), err.Error())
}

func TestStateAdmissionPropagatesCancellation(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	releaseAdmission, err := svc.AcquireStateAdmission(t.Context())
	require.NoError(t, err)
	defer releaseAdmission()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = svc.VerifyDetachedMaps(ctx, shamap.New(shamap.TypeState), nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStateAdmissionPinsDurableMutationUntilRelease(t *testing.T) {
	db := newTestNodeStore(t, 0)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc := &Service{nodeStore: db}
	releaseAdmission, err := svc.AcquireStateAdmission(t.Context())
	require.NoError(t, err)

	finished := make(chan struct{})
	go func() {
		_, _ = db.DeleteBefore(context.Background(), 1, 1)
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("durable mutation crossed an active state admission")
	case <-time.After(50 * time.Millisecond):
	}

	childRelease, err := svc.AcquireStateAdmission(t.Context())
	require.NoError(t, err)
	childRelease()
	select {
	case <-finished:
		t.Fatal("nested admission released the outer durable pin")
	case <-time.After(50 * time.Millisecond):
	}

	releaseAdmission()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("durable mutation did not proceed after admission release")
	}
}

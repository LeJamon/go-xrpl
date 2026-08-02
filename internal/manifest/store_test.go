package manifest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreReplaceLoadAndNamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	want := StoredManifests{
		Validators: [][]byte{{0x01}, {0x02, 0x03}},
		Publishers: [][]byte{{0x04, 0x05, 0x06}},
	}
	require.NoError(t, store.Replace(ctx, want))
	got, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)

	got.Validators[0][0] = 0xff
	again, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, want, again)
}

func TestSQLiteStoreReplaceRollsBackBothNamespaces(t *testing.T) {
	ctx := context.Background()
	storeAPI, err := OpenSQLiteStore(ctx, t.TempDir())
	require.NoError(t, err)
	store := storeAPI.(*sqliteStore)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	original := StoredManifests{Validators: [][]byte{{0x10}}, Publishers: [][]byte{{0x20}}}
	require.NoError(t, store.Replace(ctx, original))
	_, err = store.db.ExecContext(ctx, `CREATE TRIGGER reject_publisher
		BEFORE INSERT ON PublisherManifests
		BEGIN SELECT RAISE(ABORT, 'injected publisher failure'); END`)
	require.NoError(t, err)

	err = store.Replace(ctx, StoredManifests{Validators: [][]byte{{0x11}}, Publishers: [][]byte{{0x21}}})
	require.ErrorContains(t, err, "injected publisher failure")
	got, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestSQLiteStoreErrorsAreVisible(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err := OpenSQLiteStore(ctx, file)
	require.Error(t, err)

	storeAPI, err := OpenSQLiteStore(ctx, t.TempDir())
	require.NoError(t, err)
	store := storeAPI.(*sqliteStore)
	require.NoError(t, store.Close())
	_, err = store.Load(ctx)
	require.ErrorContains(t, err, "closed")
	require.ErrorContains(t, store.Replace(ctx, StoredManifests{}), "closed")
	require.NoError(t, store.Close())
}

func TestSQLiteStoreConcurrentClose(t *testing.T) {
	ctx := context.Background()
	storeAPI, err := OpenSQLiteStore(ctx, t.TempDir())
	require.NoError(t, err)
	store := storeAPI.(*sqliteStore)
	require.NoError(t, store.Replace(ctx, StoredManifests{Validators: [][]byte{{0x01}}}))

	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, err := store.Load(ctx)
				errs <- err
				return
			}
			errs <- store.Replace(ctx, StoredManifests{Publishers: [][]byte{{byte(i)}}})
		}()
	}
	close(start)
	require.NoError(t, store.Close())
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			require.ErrorContains(t, err, "closed")
		}
	}
	require.NoError(t, store.Close())
}

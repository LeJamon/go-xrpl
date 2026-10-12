package adaptor

import (
	"context"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

type stalledLedgerRepository struct {
	relationaldb.LedgerRepository
	release <-chan struct{}
	reads   chan<- []byte
}

func (r stalledLedgerRepository) GetLedgerInfoByHash(ctx context.Context, hash relationaldb.Hash) (*relationaldb.LedgerInfo, error) {
	select {
	case r.reads <- debug.Stack():
	default:
	}
	select {
	case <-r.release:
		return r.LedgerRepository.GetLedgerInfoByHash(ctx, hash)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type stalledLedgerManager struct {
	relationaldb.RepositoryManager
	ledger relationaldb.LedgerRepository
}

func (m stalledLedgerManager) Ledger() relationaldb.LedgerRepository { return m.ledger }

// A storage-backed service matters here: an in-memory-only service returns
// immediately on cold hashes and hides synchronous SQLite access in the router.
func TestRouter_ValidationSchedulingDoesNotWaitForSQLite(t *testing.T) {
	for _, scenario := range []string{"forward", "forward-standard", "distant", "same-sequence", "quorum", "consensus-lookup", "startup-finality"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
			require.NoError(t, err)
			rm, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
			require.NoError(t, err)
			release := make(chan struct{})
			reads := make(chan []byte, 16)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			svc, err := service.New(service.Config{
				Standalone: scenario != "startup-finality", GenesisConfig: genesis.DefaultConfig(),
				NodeStore: db, SHAMapFamily: backend.New(db),
				RelationalDB: stalledLedgerManager{rm, stalledLedgerRepository{rm.Ledger(), release, reads}},
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			a, sender := newRecordingAdaptor(t, svc)
			if scenario == "forward-standard" {
				sender.peerSupportsReplay = false
			}
			r := newTestRouter(nil, a, make(chan *peermanagement.InboundMessage, 1))
			t.Cleanup(func() {
				unblock()
				r.stopLifecycle()
				svc.Stop()
				require.NoError(t, rm.Close())
				require.NoError(t, db.Close())
			})
			closed := svc.GetClosedLedger()
			trusted, err := a.GetValidatorKey()
			require.NoError(t, err)
			seq, hash := closed.Sequence()+1, [32]byte{0xc3, 0x71}
			switch scenario {
			case "distant":
				seq += 10000
			case "same-sequence", "quorum":
				_, err = svc.AcceptConsensusResult(ctx, closed, nil, nil, time.Now(), true)
				require.NoError(t, err)
				seq = svc.GetClosedLedgerIndex()
			}
			r.catchupReplay.recordSeqHash(seq, hash, closed.Hash(), true)
			trackCatchupPeer(r, 7, seq, hash)
			if scenario == "startup-finality" {
				require.True(t, svc.NeedsInitialSync())
			}
			var missing consensus.Ledger
			var lookupErr error
			done := make(chan struct{})
			go func() {
				switch scenario {
				case "quorum":
					r.catchupReplay.armValidatedLedgerAcquisition(seq, hash)
				case "consensus-lookup":
					missing, lookupErr = a.GetLedger(consensus.LedgerID(hash))
				case "startup-finality":
					a.OnLedgerFullyValidated(consensus.LedgerID(hash), seq)
				default:
					r.catchupReplay.maybeAcquireFromValidation(&consensus.Validation{
						NodeID: trusted, LedgerSeq: seq, LedgerID: consensus.LedgerID(hash),
					}, 7)
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				unblock()
				<-done
				for len(reads) > 0 {
					t.Log(string(<-reads))
				}
				t.Fatal("validation scheduling waited for SQLite")
			}
			if scenario == "consensus-lookup" {
				require.Nil(t, missing)
				require.Error(t, lookupErr, "unknown consensus ledger must remain unavailable")
				held, err := a.GetLedger(consensus.LedgerID(closed.Hash()))
				require.NoError(t, err)
				require.Equal(t, consensus.LedgerID(closed.Hash()), held.ID())
			} else {
				require.Positive(t, acquireCount(sender), "a storage miss must still schedule acquisition")
			}
		})
	}
}

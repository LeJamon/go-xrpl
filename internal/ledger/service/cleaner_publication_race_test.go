package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

func TestCleanerRepairDuringValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sameHeight bool
	}{{"replacement", true}, {"successor", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			db := newGatedStore(t)
			db.bypass.Store(true)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			repos := newTestRepositories(t, ctx)
			svc := startPersistedTestService(t, db, repos)
			t.Cleanup(db.open)

			const seq = 27
			const oldAccount = "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59"
			const newAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
			original, oldTx := makeStubLedgerWithTransaction(t, seq, [32]byte{0xA1}, [32]byte{0x26}, oldAccount, 1)
			nextSeq, parent := uint32(seq+1), original.Hash()
			if tc.sameHeight {
				nextSeq, parent = seq, original.ParentHash()
			}
			next, newTx := makeStubLedgerWithTransaction(t, nextSeq, [32]byte{0xB1}, parent, newAccount, 2)

			svc.mu.Lock()
			svc.historyComponent.mu.Lock()
			svc.validatedLedger = original
			svc.closedLedger = original
			svc.networkLedgerState = networkLedgerFastLoadProvisional
			svc.putHistoryLocked(original)
			svc.historyComponent.mu.Unlock()
			svc.mu.Unlock()
			_, err := svc.RepairCleanerLedgerIndex(ctx, seq, original.Hash(), original.ParentHash())
			require.NoError(t, err)
			require.NoError(t, svc.persistValidatedLedger(ctx, original, true))

			db.bypass.Store(false)
			cleaned := make(chan error, 1)
			go func() {
				cleaned <- svc.RepairLedgerTransactions(ctx, seq, original.Hash(), original.ParentHash())
			}()
			waitRetrySignal(t, db.entered)
			db.bypass.Store(true)

			svc.mu.Lock()
			svc.historyComponent.mu.Lock()
			svc.closedLedger = next
			svc.putHistoryLocked(next)
			svc.historyComponent.mu.Unlock()
			svc.mu.Unlock()
			published := make(chan struct{})
			go func() {
				svc.SetValidatedLedger(nextSeq, next.Hash())
				svc.FlushPersists()
				close(published)
			}()
			waitRetrySignal(t, published)
			require.Equal(t, next.Hash(), svc.GetValidatedLedger().Hash())
			db.open()
			select {
			case err := <-cleaned:
				if tc.sameHeight {
					require.ErrorContains(t, err, "no longer canonical")
				} else {
					require.NoError(t, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleaner did not finish after the node-store write was released")
			}

			tip, err := db.Fetch(ctx, validatedTipKey)
			require.NoError(t, err)
			require.NotNil(t, tip)
			nextHash := next.Hash()
			require.Equal(t, nextHash[:], tip.Data)
			info, err := repos.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(nextSeq))
			require.NoError(t, err)
			require.Equal(t, nextHash, [32]byte(info.Hash))
			checkTransaction := func(account string, id [32]byte, seq uint32, want bool) {
				t.Helper()
				row, _, err := repos.Transaction().GetTransaction(ctx, relationaldb.Hash(id), nil)
				require.NoError(t, err)
				page, err := repos.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
					Account: accountIDFromAddress(t, account), MinLedger: relationaldb.LedgerIndex(seq),
					MaxLedger: relationaldb.LedgerIndex(seq), Limit: 10,
				})
				require.NoError(t, err)
				if !want {
					require.Nil(t, row)
					require.Empty(t, page.Transactions)
					return
				}
				require.NotNil(t, row)
				require.Equal(t, relationaldb.LedgerIndex(seq), row.LedgerSeq)
				require.Len(t, page.Transactions, 1)
				require.Equal(t, relationaldb.Hash(id), page.Transactions[0].Hash)
			}
			checkTransaction(oldAccount, oldTx, seq, !tc.sameHeight)
			checkTransaction(newAccount, newTx, nextSeq, true)
		})
	}
}

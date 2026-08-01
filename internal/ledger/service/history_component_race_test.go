package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistoryQueriesRaceWithLedgerClose(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	const closes = 48
	done := make(chan struct{})
	errs := make(chan error, 1)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}

				closed := svc.GetClosedLedger()
				if closed == nil {
					report(errors.New("closed ledger disappeared"))
					continue
				}
				seq := closed.Sequence()
				if _, err := svc.GetLedgerBySequence(seq); err != nil {
					report(err)
				}
				if _, err := svc.GetLedgerByHash(closed.Hash()); err != nil {
					report(err)
				}
				if _, err := svc.GetLedgerRange(context.Background(), seq, seq); err != nil {
					report(err)
				}
				if _, _, ok := svc.AvailableLedgerRange(); !ok {
					report(errors.New("history range disappeared"))
				}
				if _, _, err := svc.resolveLedgerForQuery(
					context.Background(),
					strconv.FormatUint(uint64(seq), 10),
				); err != nil {
					report(err)
				}
			}
		}()
	}

	for range closes {
		if _, err := svc.AcceptLedger(context.Background()); err != nil {
			close(done)
			readers.Wait()
			require.NoError(t, err)
		}
	}
	close(done)
	readers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

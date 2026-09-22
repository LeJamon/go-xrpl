package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

const publicationTestTimeout = 30 * time.Second

var errPublicationHook = errors.New("publication hook injected")

func publicationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), publicationTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

func openPublicationManagers(t *testing.T) (string, *RepositoryManager, *RepositoryManager) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), publicationTestTimeout)
	defer cancel()

	writer, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Errorf("close observer: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close writer: %v", err)
		}
	})
	return dir, writer, observer
}

func publicationAccount(first, last byte) relationaldb.AccountID {
	var account relationaldb.AccountID
	account[0] = first
	account[1] = 0x9e
	account[len(account)/2] = 0x37
	account[len(account)-2] = 0xc4
	account[len(account)-1] = last
	return account
}

func publicationValue(seq uint32, ledgerByte, transactionByte byte, accounts ...relationaldb.AccountID) relationaldb.ValidatedLedger {
	value := makePersistValue(seq)
	value.Ledger.Hash[0] = ledgerByte
	value.Ledger.ParentHash[0] = ledgerByte - 1
	value.Ledger.AccountHash[1] = ledgerByte
	value.Ledger.TransactionHash[2] = ledgerByte
	value.Transactions[0].Transaction.Hash[0] = transactionByte
	value.Transactions[0].Transaction.RawTxn = []byte{transactionByte, ledgerByte, 0x01}
	value.Transactions[0].Transaction.TxnMeta = []byte{0x02, ledgerByte, transactionByte}
	value.Transactions[0].Accounts = accounts
	return value
}

func persistWithPublicationFailure(
	ctx context.Context,
	writer *RepositoryManager,
	targetStage string,
	targetIndex int,
	observe func() error,
	value relationaldb.ValidatedLedger,
) (persistErr, observationErr error) {
	var hookCalls int
	writer.persistHook = func(stage string, index int) error {
		if stage != targetStage || index != targetIndex {
			return nil
		}
		hookCalls++
		observationErr = observe()
		return errPublicationHook
	}
	persistErr = writer.PersistValidatedLedger(ctx, value)
	writer.persistHook = nil
	if hookCalls != 1 && observationErr == nil {
		observationErr = fmt.Errorf("publication hook calls = %d, want 1", hookCalls)
	}
	return persistErr, observationErr
}

func TestPersistValidatedLedgerPublicationHooksHideNewRows(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage string
		index int
	}{
		{name: "mid-index", stage: "index", index: 1},
		{name: "ledger", stage: "ledger", index: 1},
		{name: "commit", stage: "commit", index: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := publicationContext(t)
			_, writer, observer := openPublicationManagers(t)
			value := makePersistValue(70)

			persistErr, observationErr := persistWithPublicationFailure(
				ctx,
				writer,
				test.stage,
				test.index,
				func() error { return assertNoPublicationRows(ctx, observer, value) },
				value,
			)
			if !errors.Is(persistErr, errPublicationHook) {
				t.Fatalf("PersistValidatedLedger() error = %v, want injected error", persistErr)
			}
			if observationErr != nil {
				t.Fatalf("observer saw partial publication: %v", observationErr)
			}
			if err := assertNoPublicationRows(ctx, observer, value); err != nil {
				t.Fatalf("rows survived failed publication: %v", err)
			}
		})
	}
}

func TestPersistValidatedLedgerReplacementFailurePreservesPublishedValue(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage string
		index int
	}{
		{name: "mid-index", stage: "index", index: 1},
		{name: "ledger", stage: "ledger", index: 1},
		{name: "commit", stage: "commit", index: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := publicationContext(t)
			_, writer, observer := openPublicationManagers(t)
			original := publicationValue(
				71,
				0x71,
				0x72,
				publicationAccount(0xa1, 0xb1),
				publicationAccount(0xa2, 0xb2),
			)
			if err := writer.PersistValidatedLedger(ctx, original); err != nil {
				t.Fatal(err)
			}
			if err := assertPublishedPublication(ctx, observer, original); err != nil {
				t.Fatalf("original publication: %v", err)
			}

			replacement := publicationValue(
				71,
				0xe1,
				0xe2,
				publicationAccount(0xc1, 0xd1),
				publicationAccount(0xc2, 0xd2),
			)
			persistErr, observationErr := persistWithPublicationFailure(
				ctx,
				writer,
				test.stage,
				test.index,
				func() error { return assertPublishedPublication(ctx, observer, original) },
				replacement,
			)
			if !errors.Is(persistErr, errPublicationHook) {
				t.Fatalf("PersistValidatedLedger() error = %v, want injected error", persistErr)
			}
			if observationErr != nil {
				t.Fatalf("published replacement became visible during failure: %v", observationErr)
			}
			if err := assertPublishedPublication(ctx, observer, original); err != nil {
				t.Fatalf("published replacement changed after failure: %v", err)
			}
		})
	}
}

func TestPersistValidatedLedgerRetryPublishesExactlyOnce(t *testing.T) {
	ctx := publicationContext(t)
	_, writer, observer := openPublicationManagers(t)
	original := publicationValue(
		72,
		0x72,
		0x73,
		publicationAccount(0x11, 0x21),
		publicationAccount(0x12, 0x22),
	)
	if err := writer.PersistValidatedLedger(ctx, original); err != nil {
		t.Fatal(err)
	}

	failedReplacement := publicationValue(
		72,
		0x82,
		0x83,
		publicationAccount(0x31, 0x41),
		publicationAccount(0x32, 0x42),
	)
	persistErr, observationErr := persistWithPublicationFailure(
		ctx,
		writer,
		"ledger",
		1,
		func() error { return assertPublishedPublication(ctx, observer, original) },
		failedReplacement,
	)
	if !errors.Is(persistErr, errPublicationHook) {
		t.Fatalf("failed replacement error = %v, want injected error", persistErr)
	}
	if observationErr != nil {
		t.Fatalf("published ledger changed during failed replacement: %v", observationErr)
	}

	replacement := publicationValue(72, 0x92, 0x93, publicationAccount(0x51, 0x61))
	for attempt := 0; attempt < 2; attempt++ {
		if err := writer.PersistValidatedLedger(ctx, replacement); err != nil {
			t.Fatalf("retry %d: %v", attempt+1, err)
		}
	}
	if err := assertPublishedPublication(ctx, observer, replacement); err != nil {
		t.Fatalf("replacement publication: %v", err)
	}
	for _, stale := range []relationaldb.ValidatedLedger{original, failedReplacement} {
		if err := assertAbsentPublicationIndexes(ctx, observer, stale); err != nil {
			t.Fatalf("stale publication remains: %v", err)
		}
	}
}

func TestPersistValidatedLedgerHeaderConstraintFailureRollsBack(t *testing.T) {
	ctx := publicationContext(t)
	_, writer, observer := openPublicationManagers(t)
	original := publicationValue(77, 0x77, 0x78, publicationAccount(0x17, 0x27))
	neighbor := publicationValue(76, 0x76, 0x79, publicationAccount(0x16, 0x26))
	if err := writer.PersistValidatedLedger(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := writer.PersistValidatedLedger(ctx, neighbor); err != nil {
		t.Fatal(err)
	}

	replacement := publicationValue(77, 0x87, 0x88, publicationAccount(0x37, 0x47))
	replacement.Ledger.Hash = neighbor.Ledger.Hash
	if err := writer.PersistValidatedLedger(ctx, replacement); err == nil {
		t.Fatal("constraint-conflicting header unexpectedly persisted")
	}
	if err := assertPublishedPublicationTarget(ctx, observer, original); err != nil {
		t.Fatalf("original publication after header failure: %v", err)
	}
	if err := assertPublishedPublicationTarget(ctx, observer, neighbor); err != nil {
		t.Fatalf("neighbor publication after header failure: %v", err)
	}
	if err := assertAbsentPublicationIndexes(ctx, observer, replacement); err != nil {
		t.Fatalf("failed replacement left indexes: %v", err)
	}

	replacement.Ledger.Hash[0] = 0x97
	if err := writer.PersistValidatedLedger(ctx, replacement); err != nil {
		t.Fatalf("retry after header failure: %v", err)
	}
	if err := assertPublishedPublicationTarget(ctx, observer, neighbor); err != nil {
		t.Fatalf("neighbor changed after replacement retry: %v", err)
	}
	if err := assertPublishedPublicationTarget(ctx, observer, replacement); err != nil {
		t.Fatalf("replacement after header failure: %v", err)
	}
	if err := assertAbsentPublicationIndexes(ctx, observer, original); err != nil {
		t.Fatalf("original indexes remain after replacement retry: %v", err)
	}
}

func TestPersistValidatedLedgerZeroTransactionReplacementRemovesIndexes(t *testing.T) {
	ctx := publicationContext(t)
	_, writer, observer := openPublicationManagers(t)
	original := publicationValue(
		73,
		0x73,
		0x74,
		publicationAccount(0x71, 0x81),
		publicationAccount(0x72, 0x82),
	)
	if err := writer.PersistValidatedLedger(ctx, original); err != nil {
		t.Fatal(err)
	}

	replacement := original
	replacement.Ledger.Hash[0] = 0xa3
	replacement.Ledger.AccountHash[1] = 0xa3
	replacement.Ledger.TransactionHash = relationaldb.Hash{}
	replacement.Transactions = nil
	if err := writer.PersistValidatedLedger(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := assertPublishedPublication(ctx, observer, replacement); err != nil {
		t.Fatalf("zero-transaction replacement: %v", err)
	}
	if err := assertAbsentPublicationIndexes(ctx, observer, original); err != nil {
		t.Fatalf("old indexes remain after zero-transaction replacement: %v", err)
	}
}

func TestPersistValidatedLedgerFailureReopensConsistently(t *testing.T) {
	ctx := publicationContext(t)
	dir, writer, observer := openPublicationManagers(t)
	value := makePersistValue(74)
	persistErr, observationErr := persistWithPublicationFailure(
		ctx,
		writer,
		"ledger",
		1,
		func() error { return assertNoPublicationRows(ctx, observer, value) },
		value,
	)
	if !errors.Is(persistErr, errPublicationHook) {
		t.Fatalf("PersistValidatedLedger() error = %v, want injected error", persistErr)
	}
	if observationErr != nil {
		t.Fatalf("observer saw failed publication: %v", observationErr)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := assertNoPublicationRows(ctx, reopened, value); err != nil {
		t.Fatalf("reopened repository contains failed publication: %v", err)
	}
}

func TestPersistValidatedLedgerProcessExitRecovers(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage string
		mode  string
	}{
		{name: "new-ledger", stage: "ledger", mode: "new"},
		{name: "new-commit", stage: "commit", mode: "new"},
		{name: "replacement-ledger", stage: "ledger", mode: "replacement"},
		{name: "replacement-commit", stage: "commit", mode: "replacement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := publicationContext(t)
			dir := t.TempDir()
			var original relationaldb.ValidatedLedger
			if test.mode == "replacement" {
				original = publicationValue(75, 0x75, 0x76, publicationAccount(0x15, 0x25))
				manager, err := NewRepositoryManager(ctx, dir, Settings{})
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.PersistValidatedLedger(ctx, original); err != nil {
					_ = manager.Close()
					t.Fatal(err)
				}
				if err := manager.Close(); err != nil {
					t.Fatal(err)
				}
			}

			runPublicationCrashChild(t, dir, test.stage, test.mode)
			reopened, err := NewRepositoryManager(ctx, dir, Settings{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })

			if test.mode == "new" {
				failed := makePersistValue(75)
				if err := assertNoPublicationRows(ctx, reopened, failed); err != nil {
					t.Fatalf("reopened repository contains crashed publication: %v", err)
				}
				if err := reopened.PersistValidatedLedger(ctx, failed); err != nil {
					t.Fatalf("retry after process exit: %v", err)
				}
				if err := assertPublishedPublication(ctx, reopened, failed); err != nil {
					t.Fatalf("retried publication: %v", err)
				}
				return
			}

			if err := assertPublishedPublication(ctx, reopened, original); err != nil {
				t.Fatalf("reopened original after crashed replacement: %v", err)
			}
			replacement := publicationValue(75, 0xe5, 0xe6, publicationAccount(0x35, 0x45))
			if err := reopened.PersistValidatedLedger(ctx, replacement); err != nil {
				t.Fatalf("replacement retry after process exit: %v", err)
			}
			if err := assertPublishedPublication(ctx, reopened, replacement); err != nil {
				t.Fatalf("retried replacement: %v", err)
			}
			if err := assertAbsentPublicationIndexes(ctx, reopened, original); err != nil {
				t.Fatalf("original indexes remain after replacement retry: %v", err)
			}
		})
	}
}

func TestPersistValidatedLedgerCrashHelper(t *testing.T) {
	stage := os.Getenv("GOXRPL_PUBLICATION_CRASH_STAGE")
	if stage == "" {
		return
	}
	mode := os.Getenv("GOXRPL_PUBLICATION_CRASH_MODE")
	ctx, cancel := context.WithTimeout(context.Background(), publicationTestTimeout)
	defer cancel()
	manager, err := NewRepositoryManager(ctx, ".", Settings{})
	if err != nil {
		os.Exit(2)
	}
	value := makePersistValue(75)
	if mode == "replacement" {
		value = publicationValue(75, 0xe5, 0xe6, publicationAccount(0x35, 0x45))
	}
	manager.persistHook = func(hookStage string, index int) error {
		if hookStage == stage && index == 1 {
			os.Exit(0)
		}
		return nil
	}
	if err := manager.PersistValidatedLedger(ctx, value); err != nil {
		os.Exit(3)
	}
	os.Exit(4)
}

func runPublicationCrashChild(t *testing.T, dir, stage, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), publicationTestTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersistValidatedLedgerCrashHelper$")
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GOXRPL_PUBLICATION_CRASH_MODE="+mode,
		"GOXRPL_PUBLICATION_CRASH_STAGE="+stage,
	)
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("crash helper exceeded deadline: %v", ctx.Err())
		}
		t.Fatalf("crash helper error: %v", err)
	}
}

func assertNoPublicationRows(ctx context.Context, rm *RepositoryManager, value relationaldb.ValidatedLedger) error {
	ledger := rm.Ledger()
	bySequence, err := ledger.GetLedgerInfoBySeq(ctx, value.Ledger.Sequence)
	if bySequence != nil || !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		return fmt.Errorf("ledger by sequence = %v, %v; want not found", bySequence, err)
	}
	byHash, err := ledger.GetLedgerInfoByHash(ctx, value.Ledger.Hash)
	if byHash != nil || !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		return fmt.Errorf("ledger by hash = %v, %v; want not found", byHash, err)
	}
	newest, err := ledger.GetNewestLedgerInfo(ctx)
	if err != nil || newest != nil {
		return fmt.Errorf("newest ledger = %v, %v; want nil, nil", newest, err)
	}
	hashes, err := ledger.GetHashesByRange(ctx, value.Ledger.Sequence, value.Ledger.Sequence)
	if err != nil || len(hashes) != 0 {
		return fmt.Errorf("ledger range = %v, %v; want empty, nil", hashes, err)
	}
	minLedger, err := ledger.GetMinLedgerSeq(ctx)
	if err != nil || minLedger != nil {
		return fmt.Errorf("minimum ledger = %v, %v; want nil, nil", minLedger, err)
	}
	maxLedger, err := ledger.GetMaxLedgerSeq(ctx)
	if err != nil || maxLedger != nil {
		return fmt.Errorf("maximum ledger = %v, %v; want nil, nil", maxLedger, err)
	}

	transaction := rm.Transaction()
	got, searched, err := transaction.GetTransaction(ctx, value.Transactions[0].Transaction.Hash, nil)
	if err != nil || got != nil || searched != relationaldb.TxSearchUnknown {
		return fmt.Errorf("transaction lookup = %v, %v, %v; want nil, unknown, nil", got, searched, err)
	}
	got, searched, err = transaction.GetTransaction(ctx, value.Transactions[0].Transaction.Hash, &relationaldb.LedgerRange{
		Min: value.Ledger.Sequence,
		Max: value.Ledger.Sequence,
	})
	if err != nil || got != nil || searched != relationaldb.TxSearchSome {
		return fmt.Errorf("transaction range lookup = %v, %v, %v; want nil, some, nil", got, searched, err)
	}
	history, err := transaction.GetTxHistory(ctx, 0, 100)
	if err != nil || len(history) != 0 {
		return fmt.Errorf("transaction history = %v, %v; want empty, nil", history, err)
	}
	minTransaction, err := transaction.GetTransactionsMinLedgerSeq(ctx)
	if err != nil || minTransaction != nil {
		return fmt.Errorf("minimum transaction ledger = %v, %v; want nil, nil", minTransaction, err)
	}

	accountTransaction := rm.AccountTransaction()
	minAccount, err := accountTransaction.GetAccountTransactionsMinLedgerSeq(ctx)
	if err != nil || minAccount != nil {
		return fmt.Errorf("minimum account ledger = %v, %v; want nil, nil", minAccount, err)
	}
	for _, indexed := range value.Transactions {
		for _, account := range indexed.Accounts {
			options := relationaldb.AccountTxPageOptions{
				Account:   account,
				MinLedger: value.Ledger.Sequence,
				MaxLedger: value.Ledger.Sequence,
				Limit:     100,
			}
			oldest, err := accountTransaction.GetOldestAccountTxsPage(ctx, options)
			if err != nil {
				return fmt.Errorf("oldest account page: %w", err)
			}
			if err := assertEmptyAccountPage(oldest, options); err != nil {
				return fmt.Errorf("oldest account page: %w", err)
			}
			newest, err := accountTransaction.GetNewestAccountTxsPage(ctx, options)
			if err != nil {
				return fmt.Errorf("newest account page: %w", err)
			}
			if err := assertEmptyAccountPage(newest, options); err != nil {
				return fmt.Errorf("newest account page: %w", err)
			}
		}
	}
	return nil
}

func assertPublishedPublication(ctx context.Context, rm *RepositoryManager, value relationaldb.ValidatedLedger) error {
	if err := assertPublishedPublicationTarget(ctx, rm, value); err != nil {
		return err
	}
	ledger := rm.Ledger()
	newest, err := ledger.GetNewestLedgerInfo(ctx)
	if err != nil {
		return fmt.Errorf("newest ledger: %w", err)
	}
	if !samePublicationLedger(newest, &value.Ledger) {
		return fmt.Errorf("newest ledger = %+v, want %+v", newest, value.Ledger)
	}
	hashes, err := ledger.GetHashesByRange(ctx, value.Ledger.Sequence, value.Ledger.Sequence)
	if err != nil {
		return fmt.Errorf("ledger range: %w", err)
	}
	pair, ok := hashes[value.Ledger.Sequence]
	if len(hashes) != 1 || !ok || pair.LedgerHash != value.Ledger.Hash || pair.ParentHash != value.Ledger.ParentHash {
		return fmt.Errorf("ledger range = %v, want one exact hash pair", hashes)
	}
	minLedger, err := ledger.GetMinLedgerSeq(ctx)
	if err != nil || minLedger == nil || *minLedger != value.Ledger.Sequence {
		return fmt.Errorf("minimum ledger = %v, %v; want %d", minLedger, err, value.Ledger.Sequence)
	}
	maxLedger, err := ledger.GetMaxLedgerSeq(ctx)
	if err != nil || maxLedger == nil || *maxLedger != value.Ledger.Sequence {
		return fmt.Errorf("maximum ledger = %v, %v; want %d", maxLedger, err, value.Ledger.Sequence)
	}

	transaction := rm.Transaction()
	history, err := transaction.GetTxHistory(ctx, 0, 100)
	if err != nil {
		return fmt.Errorf("transaction history: %w", err)
	}
	if len(history) != len(value.Transactions) {
		return fmt.Errorf("transaction history count = %d, want %d", len(history), len(value.Transactions))
	}
	minTransaction, err := transaction.GetTransactionsMinLedgerSeq(ctx)
	if len(value.Transactions) == 0 {
		if err != nil || minTransaction != nil || len(history) != 0 {
			return fmt.Errorf("empty transaction indexes = min %v, err %v, history %v", minTransaction, err, history)
		}
	} else if err != nil || minTransaction == nil || *minTransaction != value.Ledger.Sequence {
		return fmt.Errorf("minimum transaction ledger = %v, %v; want %d", minTransaction, err, value.Ledger.Sequence)
	}
	for index, indexed := range value.Transactions {
		if !samePublicationTransaction(&history[index], &indexed.Transaction, true) {
			return fmt.Errorf("transaction history %d = %+v, want %+v", index, history[index], indexed.Transaction)
		}
	}

	accountTransaction := rm.AccountTransaction()
	if len(value.Transactions) == 0 {
		minAccount, err := accountTransaction.GetAccountTransactionsMinLedgerSeq(ctx)
		if err != nil || minAccount != nil {
			return fmt.Errorf("minimum account ledger = %v, %v; want nil, nil", minAccount, err)
		}
		return assertPublicationAccountRowCount(ctx, rm, value)
	}
	minAccount, err := accountTransaction.GetAccountTransactionsMinLedgerSeq(ctx)
	if err != nil || minAccount == nil || *minAccount != value.Ledger.Sequence {
		return fmt.Errorf("minimum account ledger = %v, %v; want %d", minAccount, err, value.Ledger.Sequence)
	}
	return assertPublicationAccountRowCount(ctx, rm, value)
}

func assertPublicationAccountRowCount(ctx context.Context, rm *RepositoryManager, value relationaldb.ValidatedLedger) error {
	var accountRows int64
	if err := rm.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_transactions").Scan(&accountRows); err != nil {
		return fmt.Errorf("account index count: %w", err)
	}
	wantAccountRows := 0
	for _, indexed := range value.Transactions {
		wantAccountRows += len(indexed.Accounts)
	}
	if accountRows != int64(wantAccountRows) {
		return fmt.Errorf("account index row count = %d, want %d", accountRows, wantAccountRows)
	}
	return nil
}

func assertPublishedPublicationTarget(ctx context.Context, rm *RepositoryManager, value relationaldb.ValidatedLedger) error {
	ledger := rm.Ledger()
	bySequence, err := ledger.GetLedgerInfoBySeq(ctx, value.Ledger.Sequence)
	if err != nil {
		return fmt.Errorf("ledger by sequence: %w", err)
	}
	if !samePublicationLedger(bySequence, &value.Ledger) {
		return fmt.Errorf("ledger by sequence = %+v, want %+v", bySequence, value.Ledger)
	}
	byHash, err := ledger.GetLedgerInfoByHash(ctx, value.Ledger.Hash)
	if err != nil {
		return fmt.Errorf("ledger by hash: %w", err)
	}
	if !samePublicationLedger(byHash, &value.Ledger) {
		return fmt.Errorf("ledger by hash = %+v, want %+v", byHash, value.Ledger)
	}
	for index, indexed := range value.Transactions {
		got, searched, err := rm.Transaction().GetTransaction(ctx, indexed.Transaction.Hash, nil)
		if err != nil || searched != relationaldb.TxSearchAll || !samePublicationTransaction(got, &indexed.Transaction, false) {
			return fmt.Errorf("transaction %d = %v, searched %v, err %v; want exact row", index, got, searched, err)
		}
		for accountIndex, account := range indexed.Accounts {
			options := relationaldb.AccountTxPageOptions{Account: account, Limit: 100}
			oldest, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, options)
			if err != nil {
				return fmt.Errorf("transaction %d account %d oldest page: %w", index, accountIndex, err)
			}
			if err := assertAccountPage(oldest, options, indexed.Transaction); err != nil {
				return fmt.Errorf("transaction %d account %d oldest page: %w", index, accountIndex, err)
			}
			newest, err := rm.AccountTransaction().GetNewestAccountTxsPage(ctx, options)
			if err != nil {
				return fmt.Errorf("transaction %d account %d newest page: %w", index, accountIndex, err)
			}
			if err := assertAccountPage(newest, options, indexed.Transaction); err != nil {
				return fmt.Errorf("transaction %d account %d newest page: %w", index, accountIndex, err)
			}
		}
	}
	return nil
}

func assertAbsentPublicationIndexes(ctx context.Context, rm *RepositoryManager, value relationaldb.ValidatedLedger) error {
	transaction := rm.Transaction()
	for _, indexed := range value.Transactions {
		got, searched, err := transaction.GetTransaction(ctx, indexed.Transaction.Hash, nil)
		if err != nil || got != nil || searched != relationaldb.TxSearchUnknown {
			return fmt.Errorf("stale transaction = %v, searched %v, err %v", got, searched, err)
		}
		for _, account := range indexed.Accounts {
			options := relationaldb.AccountTxPageOptions{Account: account, Limit: 100}
			oldest, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, options)
			if err != nil {
				return fmt.Errorf("stale oldest account page: %w", err)
			}
			if err := assertEmptyAccountPage(oldest, options); err != nil {
				return fmt.Errorf("stale oldest account page: %w", err)
			}
			newest, err := rm.AccountTransaction().GetNewestAccountTxsPage(ctx, options)
			if err != nil {
				return fmt.Errorf("stale newest account page: %w", err)
			}
			if err := assertEmptyAccountPage(newest, options); err != nil {
				return fmt.Errorf("stale newest account page: %w", err)
			}
		}
	}
	return nil
}

func samePublicationLedger(got, want *relationaldb.LedgerInfo) bool {
	return got != nil && want != nil &&
		got.Hash == want.Hash &&
		got.Sequence == want.Sequence &&
		got.ParentHash == want.ParentHash &&
		got.AccountHash == want.AccountHash &&
		got.TransactionHash == want.TransactionHash &&
		got.TotalCoins == want.TotalCoins &&
		got.CloseTime.Equal(want.CloseTime) &&
		got.ParentCloseTime.Equal(want.ParentCloseTime) &&
		got.CloseTimeRes == want.CloseTimeRes &&
		got.CloseFlags == want.CloseFlags
}

func samePublicationTransaction(got, want *relationaldb.TransactionInfo, includeSequence bool) bool {
	if got == nil || want == nil ||
		got.Hash != want.Hash ||
		got.LedgerSeq != want.LedgerSeq ||
		got.Status != want.Status ||
		!bytes.Equal(got.RawTxn, want.RawTxn) ||
		!bytes.Equal(got.TxnMeta, want.TxnMeta) {
		return false
	}
	return !includeSequence || got.TxnSeq == want.TxnSeq
}

func assertEmptyAccountPage(page *relationaldb.AccountTxResult, options relationaldb.AccountTxPageOptions) error {
	if page == nil {
		return errors.New("page is nil")
	}
	if len(page.Transactions) != 0 || page.Marker != nil || page.Limit != options.Limit || page.LedgerRange != (relationaldb.LedgerRange{Min: options.MinLedger, Max: options.MaxLedger}) {
		return fmt.Errorf("page = %+v, want empty page with range %d-%d and limit %d", page, options.MinLedger, options.MaxLedger, options.Limit)
	}
	return nil
}

func assertAccountPage(page *relationaldb.AccountTxResult, options relationaldb.AccountTxPageOptions, want relationaldb.TransactionInfo) error {
	if page == nil {
		return errors.New("page is nil")
	}
	if len(page.Transactions) != 1 || page.Marker != nil || page.Limit != options.Limit || page.LedgerRange != (relationaldb.LedgerRange{Min: options.MinLedger, Max: options.MaxLedger}) {
		return fmt.Errorf("page = %+v, want one exact transaction with range %d-%d and limit %d", page, options.MinLedger, options.MaxLedger, options.Limit)
	}
	got := page.Transactions[0]
	if !samePublicationTransaction(&got, &want, true) || got.Account != options.Account {
		return fmt.Errorf("page transaction = %+v, want %+v for account %x", got, want, options.Account)
	}
	return nil
}

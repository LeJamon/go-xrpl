// Package contracttest verifies relational repository backend parity.
package contracttest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Factory creates an isolated repository manager for one test.
type Factory func(*testing.T) relationaldb.RepositoryManager

// Run executes the shared relational repository contract.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("ready lifecycle", func(t *testing.T) {
		ctx := context.Background()
		manager := factory(t)
		repository := manager.Ledger()
		if err := repository.SaveValidatedLedger(ctx, ledger(1)); err != nil {
			t.Fatal(err)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.GetMaxLedgerSeq(ctx); !errors.Is(err, relationaldb.ErrDatabaseClosed) {
			t.Fatalf("retained repository error = %v, want ErrDatabaseClosed", err)
		}
	})

	t.Run("transaction ownership", func(t *testing.T) {
		ctx := context.Background()
		manager := factory(t)
		value := transaction(2)
		var retained relationaldb.TransactionRepository
		if err := manager.WithTransaction(ctx, func(repositories relationaldb.TransactionRepositories) error {
			retained = repositories.Transaction()
			return retained.SaveTransaction(ctx, value)
		}); err != nil {
			t.Fatal(err)
		}
		if err := retained.SaveTransaction(ctx, value); !errors.Is(err, relationaldb.ErrTransactionClosed) {
			t.Fatalf("retained transaction repository error = %v, want ErrTransactionClosed", err)
		}
		if err := manager.WithTransaction(ctx, func(repositories relationaldb.TransactionRepositories) error {
			value := transaction(3)
			if err := repositories.Transaction().SaveTransaction(ctx, value); err != nil {
				return err
			}
			return errors.New("rollback")
		}); err == nil {
			t.Fatal("expected rollback error")
		}
		var hash relationaldb.Hash
		hash[0] = 3
		found, _, err := manager.Transaction().GetTransaction(ctx, hash, nil)
		if err != nil || found != nil {
			t.Fatalf("rolled-back transaction found: value=%v error=%v", found, err)
		}
	})

	t.Run("close waits for transaction", func(t *testing.T) {
		ctx := context.Background()
		manager := factory(t)
		blocked := make(chan struct{})
		release := make(chan struct{})
		transactionDone := make(chan error, 1)
		go func() {
			transactionDone <- manager.WithTransaction(ctx, func(repositories relationaldb.TransactionRepositories) error {
				if err := repositories.Transaction().SaveTransaction(ctx, transaction(7)); err != nil {
					return err
				}
				close(blocked)
				<-release
				return nil
			})
		}()
		<-blocked
		closeDone := make(chan error, 1)
		go func() {
			closeDone <- manager.Close()
		}()
		WaitForTransactionRejection(t, manager)
		select {
		case err := <-closeDone:
			t.Fatalf("Close returned before transaction completed: %v", err)
		default:
		}
		close(release)
		if err := <-transactionDone; err != nil {
			t.Fatal(err)
		}
		if err := <-closeDone; err != nil {
			t.Fatal(err)
		}
		if err := manager.WithTransaction(ctx, func(relationaldb.TransactionRepositories) error {
			return nil
		}); !errors.Is(err, relationaldb.ErrDatabaseClosed) {
			t.Fatalf("post-close transaction error = %v, want ErrDatabaseClosed", err)
		}
	})

	t.Run("validated ledger aggregate", func(t *testing.T) {
		ctx := context.Background()
		manager := factory(t)
		value := validatedLedger(4)
		if err := manager.PersistValidatedLedger(ctx, value); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Ledger().GetLedgerInfoBySeq(ctx, value.Ledger.Sequence); err != nil {
			t.Fatal(err)
		}
		found, _, err := manager.Transaction().GetTransaction(ctx, value.Transactions[0].Transaction.Hash, nil)
		if err != nil || found == nil {
			t.Fatalf("transaction missing: value=%v error=%v", found, err)
		}
		page, err := manager.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
			Account: value.Transactions[0].Accounts[0],
			Limit:   1,
		})
		if err != nil || len(page.Transactions) != 1 {
			t.Fatalf("account index missing: page=%v error=%v", page, err)
		}
	})

	t.Run("aggregate preflight is non-mutating", func(t *testing.T) {
		ctx := context.Background()
		manager := factory(t)
		original := validatedLedger(5)
		original.Transactions[0].Accounts[0][0] = 5
		if err := manager.PersistValidatedLedger(ctx, original); err != nil {
			t.Fatal(err)
		}

		invalid := validatedLedger(5)
		invalid.Ledger.Hash[0] = 0xa5
		invalid.Transactions[0].Transaction.Hash[0] = 0xb5
		invalid.Transactions[0].Transaction.LedgerSeq = 6
		invalid.Transactions[0].Accounts[0][0] = 6
		if err := manager.PersistValidatedLedger(ctx, invalid); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("mismatched aggregate error = %v, want ErrInvalidData", err)
		}

		gotLedger, err := manager.Ledger().GetLedgerInfoBySeq(ctx, original.Ledger.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		if gotLedger.Hash != original.Ledger.Hash {
			t.Fatalf("ledger hash changed to %x", gotLedger.Hash)
		}
		gotTransaction, _, err := manager.Transaction().GetTransaction(ctx, original.Transactions[0].Transaction.Hash, nil)
		if err != nil || gotTransaction == nil {
			t.Fatalf("original transaction changed: value=%v error=%v", gotTransaction, err)
		}
		rejectedTransaction, _, err := manager.Transaction().GetTransaction(ctx, invalid.Transactions[0].Transaction.Hash, nil)
		if err != nil || rejectedTransaction != nil {
			t.Fatalf("rejected transaction stored: value=%v error=%v", rejectedTransaction, err)
		}
		originalPage, err := manager.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
			Account: original.Transactions[0].Accounts[0],
			Limit:   1,
		})
		if err != nil || len(originalPage.Transactions) != 1 {
			t.Fatalf("original account index changed: page=%v error=%v", originalPage, err)
		}
		rejectedPage, err := manager.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
			Account: invalid.Transactions[0].Accounts[0],
			Limit:   1,
		})
		if err != nil || len(rejectedPage.Transactions) != 0 {
			t.Fatalf("rejected account index stored: page=%v error=%v", rejectedPage, err)
		}
	})

	t.Run("empty ledger aggregate", func(t *testing.T) {
		ctx := context.Background()
		manager := factory(t)
		value := relationaldb.ValidatedLedger{Ledger: ledger(6)}
		if err := manager.PersistValidatedLedger(ctx, value); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Ledger().GetLedgerInfoBySeq(ctx, value.Ledger.Sequence); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ledger repository", func(t *testing.T) {
		runLedgerRepository(t, factory)
	})
	t.Run("transaction repository", func(t *testing.T) {
		runTransactionRepository(t, factory)
	})
	t.Run("account transaction repository", func(t *testing.T) {
		runAccountTransactionRepository(t, factory)
	})
	t.Run("validation repository", func(t *testing.T) {
		runValidationRepository(t, factory)
	})
	t.Run("amendment repository", func(t *testing.T) {
		runAmendmentRepository(t, factory)
	})
}

func runLedgerRepository(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	manager := factory(t)
	repository := manager.Ledger()

	min, err := repository.GetMinLedgerSeq(ctx)
	if err != nil || min != nil {
		t.Fatalf("empty minimum = %v, %v; want nil, nil", min, err)
	}
	max, err := repository.GetMaxLedgerSeq(ctx)
	if err != nil || max != nil {
		t.Fatalf("empty maximum = %v, %v; want nil, nil", max, err)
	}
	newest, err := repository.GetNewestLedgerInfo(ctx)
	if err != nil || newest != nil {
		t.Fatalf("empty newest = %v, %v; want nil, nil", newest, err)
	}
	if _, err := repository.GetLedgerInfoBySeq(ctx, 10); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("missing sequence error = %v, want ErrLedgerNotFound", err)
	}
	if _, err := repository.GetLedgerInfoByHash(ctx, relationaldb.Hash{10}); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("missing hash error = %v, want ErrLedgerNotFound", err)
	}

	values := []relationaldb.LedgerInfo{ledger(10), ledger(12), ledger(11)}
	for _, value := range values {
		if err := repository.SaveValidatedLedger(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	min, err = repository.GetMinLedgerSeq(ctx)
	if err != nil || min == nil || *min != 10 {
		t.Fatalf("minimum = %v, %v; want 10, nil", min, err)
	}
	max, err = repository.GetMaxLedgerSeq(ctx)
	if err != nil || max == nil || *max != 12 {
		t.Fatalf("maximum = %v, %v; want 12, nil", max, err)
	}
	newest, err = repository.GetNewestLedgerInfo(ctx)
	if err != nil || !sameLedger(newest, &values[1]) {
		t.Fatalf("newest = %+v, %v; want %+v, nil", newest, err, values[1])
	}
	bySequence, err := repository.GetLedgerInfoBySeq(ctx, 11)
	if err != nil || !sameLedger(bySequence, &values[2]) {
		t.Fatalf("by sequence = %+v, %v; want %+v, nil", bySequence, err, values[2])
	}
	byHash, err := repository.GetLedgerInfoByHash(ctx, values[0].Hash)
	if err != nil || !sameLedger(byHash, &values[0]) {
		t.Fatalf("by hash = %+v, %v; want %+v, nil", byHash, err, values[0])
	}

	hashes, err := repository.GetHashesByRange(ctx, 10, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 2 ||
		hashes[10].LedgerHash != values[0].Hash || hashes[10].ParentHash != values[0].ParentHash ||
		hashes[11].LedgerHash != values[2].Hash || hashes[11].ParentHash != values[2].ParentHash {
		t.Fatalf("hash range = %+v", hashes)
	}
	reversed, err := repository.GetHashesByRange(ctx, 12, 10)
	if err != nil || len(reversed) != 0 {
		t.Fatalf("reversed hash range = %+v, %v; want empty, nil", reversed, err)
	}

	replacement := ledger(11)
	replacement.Hash[0] = 111
	replacement.ParentHash[1] = 111
	replacement.TotalCoins++
	replacement.CloseFlags = 111
	if err := repository.SaveValidatedLedger(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetLedgerInfoBySeq(ctx, 11)
	if err != nil || !sameLedger(got, &replacement) {
		t.Fatalf("replacement = %+v, %v; want %+v, nil", got, err, replacement)
	}
	if _, err := repository.GetLedgerInfoByHash(ctx, values[2].Hash); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("replaced hash error = %v, want ErrLedgerNotFound", err)
	}

	if err := repository.DeleteLedgersBySeq(ctx, 11); err != nil {
		t.Fatal(err)
	}
	min, err = repository.GetMinLedgerSeq(ctx)
	if err != nil || min == nil || *min != 12 {
		t.Fatalf("minimum after delete = %v, %v; want 12, nil", min, err)
	}
	if _, err := repository.GetLedgerInfoBySeq(ctx, 11); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("deleted boundary error = %v, want ErrLedgerNotFound", err)
	}
	if got, err := repository.GetLedgerInfoBySeq(ctx, 12); err != nil || !sameLedger(got, &values[1]) {
		t.Fatalf("preserved ledger = %+v, %v; want %+v, nil", got, err, values[1])
	}
}

func runTransactionRepository(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	manager := factory(t)
	repository := manager.Transaction()
	accountRepository := manager.AccountTransaction()

	min, err := repository.GetTransactionsMinLedgerSeq(ctx)
	if err != nil || min != nil {
		t.Fatalf("empty minimum = %v, %v; want nil, nil", min, err)
	}
	missing := relationaldb.Hash{99}
	found, searched, err := repository.GetTransaction(ctx, missing, nil)
	if err != nil || found != nil || searched != relationaldb.TxSearchUnknown {
		t.Fatalf("missing transaction = %v, %v, %v; want nil, unknown, nil", found, searched, err)
	}

	values := []relationaldb.TransactionInfo{
		transactionAt(40, 40, 1),
		transactionAt(41, 41, 1),
		transactionAt(42, 42, 1),
	}
	values[0].TxnMeta = []byte{4, 0}
	for _, value := range values {
		if err := repository.SaveTransaction(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	min, err = repository.GetTransactionsMinLedgerSeq(ctx)
	if err != nil || min == nil || *min != 40 {
		t.Fatalf("minimum = %v, %v; want 40, nil", min, err)
	}
	found, searched, err = repository.GetTransaction(ctx, values[0].Hash, nil)
	if err != nil || searched != relationaldb.TxSearchAll || !sameStoredTransaction(found, &values[0]) {
		t.Fatalf("round trip = %+v, %v, %v; want %+v, all, nil", found, searched, err, values[0])
	}

	replacement := values[0]
	replacement.Status = "replaced"
	replacement.RawTxn = []byte{9, 8, 7}
	replacement.TxnMeta = []byte{6, 5}
	if err := repository.SaveTransaction(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	found, _, err = repository.GetTransaction(ctx, replacement.Hash, nil)
	if err != nil || !sameStoredTransaction(found, &replacement) {
		t.Fatalf("replacement = %+v, %v; want %+v, nil", found, err, replacement)
	}

	found, searched, err = repository.GetTransaction(ctx, missing, &relationaldb.LedgerRange{Min: 40, Max: 42})
	if err != nil || found != nil || searched != relationaldb.TxSearchAll {
		t.Fatalf("complete range search = %v, %v, %v; want nil, all, nil", found, searched, err)
	}
	found, searched, err = repository.GetTransaction(ctx, missing, &relationaldb.LedgerRange{Min: 40, Max: 43})
	if err != nil || found != nil || searched != relationaldb.TxSearchSome {
		t.Fatalf("partial range search = %v, %v, %v; want nil, some, nil", found, searched, err)
	}

	history, err := repository.GetTxHistory(ctx, 1, 1)
	if err != nil || len(history) != 1 || history[0].LedgerSeq != 41 {
		t.Fatalf("history = %+v, %v; want ledger 41, nil", history, err)
	}
	history, err = repository.GetTxHistory(ctx, 0, 2)
	if err != nil || len(history) != 2 || history[0].LedgerSeq != 42 || history[1].LedgerSeq != 41 {
		t.Fatalf("limited history = %+v, %v; want ledgers 42, 41", history, err)
	}

	account := accountID(1)
	for _, value := range values {
		if err := accountRepository.SaveAccountTransaction(ctx, account, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.DeleteTransactionsByLedgerSeq(ctx, 41); err != nil {
		t.Fatal(err)
	}
	if got, _, err := repository.GetTransaction(ctx, values[1].Hash, nil); err != nil || got != nil {
		t.Fatalf("exact delete left transaction: %+v, %v", got, err)
	}
	if err := repository.DeleteTransactionsBeforeLedgerSeq(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if got, _, err := repository.GetTransaction(ctx, values[0].Hash, nil); err != nil || got != nil {
		t.Fatalf("before delete left transaction: %+v, %v", got, err)
	}
	if got, _, err := repository.GetTransaction(ctx, values[2].Hash, nil); err != nil || !sameStoredTransaction(got, &values[2]) {
		t.Fatalf("boundary transaction = %+v, %v; want %+v, nil", got, err, values[2])
	}
	page, err := accountRepository.GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: account,
		Limit:   10,
	})
	if err != nil || len(page.Transactions) != 1 || page.Transactions[0].Hash != values[2].Hash {
		t.Fatalf("account indexes after transaction deletes = %+v, %v", page, err)
	}
}

func runAccountTransactionRepository(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	manager := factory(t)
	repository := manager.AccountTransaction()
	transactionRepository := manager.Transaction()

	min, err := repository.GetAccountTransactionsMinLedgerSeq(ctx)
	if err != nil || min != nil {
		t.Fatalf("empty minimum = %v, %v; want nil, nil", min, err)
	}
	account := accountID(1)
	otherAccount := accountID(2)
	values := []relationaldb.TransactionInfo{
		transactionAt(50, 10, 1),
		transactionAt(51, 10, 2),
		transactionAt(52, 11, 1),
		transactionAt(53, 12, 1),
	}
	for _, value := range values {
		if err := transactionRepository.SaveTransaction(ctx, value); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveAccountTransaction(ctx, account, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SaveAccountTransaction(ctx, account, values[0]); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	if err := repository.SaveAccountTransaction(ctx, otherAccount, values[3]); err != nil {
		t.Fatal(err)
	}
	min, err = repository.GetAccountTransactionsMinLedgerSeq(ctx)
	if err != nil || min == nil || *min != 10 {
		t.Fatalf("minimum = %v, %v; want 10, nil", min, err)
	}

	options := relationaldb.AccountTxPageOptions{
		Account:   account,
		MinLedger: 10,
		MaxLedger: 12,
		Limit:     2,
	}
	oldest, err := repository.GetOldestAccountTxsPage(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountPage(t, oldest, options, values[0], values[1])
	assertMarker(t, oldest.Marker, 10, 2)
	options.Marker = oldest.Marker
	oldest, err = repository.GetOldestAccountTxsPage(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountPage(t, oldest, options, values[2], values[3])
	if oldest.Marker != nil {
		t.Fatalf("last oldest page marker = %+v, want nil", oldest.Marker)
	}

	options.Marker = nil
	newest, err := repository.GetNewestAccountTxsPage(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountPage(t, newest, options, values[3], values[2])
	assertMarker(t, newest.Marker, 11, 1)
	options.Marker = newest.Marker
	newest, err = repository.GetNewestAccountTxsPage(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountPage(t, newest, options, values[1], values[0])
	if newest.Marker != nil {
		t.Fatalf("last newest page marker = %+v, want nil", newest.Marker)
	}

	boundedOptions := relationaldb.AccountTxPageOptions{
		Account:   account,
		MinLedger: 11,
		MaxLedger: 11,
		Limit:     10,
	}
	bounded, err := repository.GetOldestAccountTxsPage(ctx, boundedOptions)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountPage(t, bounded, boundedOptions, values[2])
	other, err := repository.GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: otherAccount,
		Limit:   10,
	})
	if err != nil || len(other.Transactions) != 1 || other.Transactions[0].Hash != values[3].Hash {
		t.Fatalf("other account page = %+v, %v", other, err)
	}

	if err := repository.DeleteAccountTransactionsBeforeLedgerSeq(ctx, 11); err != nil {
		t.Fatal(err)
	}
	remaining, err := repository.GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: account,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Transactions) != 2 ||
		remaining.Transactions[0].Hash != values[2].Hash ||
		remaining.Transactions[1].Hash != values[3].Hash {
		t.Fatalf("remaining account transactions = %+v", remaining.Transactions)
	}
	min, err = repository.GetAccountTransactionsMinLedgerSeq(ctx)
	if err != nil || min == nil || *min != 11 {
		t.Fatalf("minimum after delete = %v, %v; want 11, nil", min, err)
	}
	if got, _, err := transactionRepository.GetTransaction(ctx, values[0].Hash, nil); err != nil || got == nil {
		t.Fatalf("account delete removed base transaction: %+v, %v", got, err)
	}
}

func runValidationRepository(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	manager := factory(t)
	repository := manager.Validation()

	empty, err := repository.GetValidationsForLedger(ctx, 999)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty ledger query = %+v, %v; want empty, nil", empty, err)
	}
	first := validation(100, 1)
	if err := repository.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(ctx, first); err != nil {
		t.Fatalf("duplicate save: %v", err)
	}
	rows, err := repository.GetValidationsForLedger(ctx, 100)
	if err != nil || len(rows) != 1 || !sameValidation(rows[0], first) {
		t.Fatalf("round trip = %+v, %v; want %+v, nil", rows, err, first)
	}

	second := validation(100, 2)
	third := validation(101, 1)
	if err := repository.SaveBatch(ctx, []*relationaldb.ValidationRecord{first, second, third, second}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveBatch(ctx, nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	rows, err = repository.GetValidationsForLedger(ctx, 100)
	if err != nil || len(rows) != 2 {
		t.Fatalf("batch rows = %+v, %v; want two, nil", rows, err)
	}
	validatorRows, err := repository.GetValidationsByValidator(ctx, first.NodePubKey, 1)
	if err != nil || len(validatorRows) != 1 || validatorRows[0].LedgerSeq != 101 {
		t.Fatalf("limited validator query = %+v, %v; want ledger 101", validatorRows, err)
	}
	validatorRows, err = repository.GetValidationsByValidator(ctx, first.NodePubKey, 0)
	if err != nil || len(validatorRows) != 2 ||
		validatorRows[0].LedgerSeq != 101 || validatorRows[1].LedgerSeq != 100 {
		t.Fatalf("unbounded validator query = %+v, %v; want ledgers 101, 100", validatorRows, err)
	}
	validatorRows, err = repository.GetValidationsByValidator(ctx, first.NodePubKey, -1)
	if err != nil || len(validatorRows) != 2 {
		t.Fatalf("negative-limit validator query = %+v, %v; want two, nil", validatorRows, err)
	}

	for _, bad := range []*relationaldb.ValidationRecord{
		nil,
		validationWithKeyWidth(200, 1, 32),
		validationWithKeyWidth(200, 1, 34),
	} {
		if err := repository.Save(ctx, bad); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("malformed save error = %v, want ErrInvalidData", err)
		}
	}
	for _, badKey := range [][]byte{nil, make([]byte, 32), make([]byte, 34)} {
		if _, err := repository.GetValidationsByValidator(ctx, badKey, 1); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("malformed validator query error = %v, want ErrInvalidData", err)
		}
	}
	batchValue := validation(299, 3)
	if err := repository.SaveBatch(ctx, []*relationaldb.ValidationRecord{
		batchValue,
		validationWithKeyWidth(299, 4, 32),
	}); !errors.Is(err, relationaldb.ErrInvalidData) {
		t.Fatalf("malformed batch error = %v, want ErrInvalidData", err)
	}
	rows, err = repository.GetValidationsForLedger(ctx, 299)
	if err != nil || len(rows) != 0 {
		t.Fatalf("malformed batch was not atomic: %+v, %v", rows, err)
	}

	for seq := uint32(1); seq <= 5; seq++ {
		if err := repository.Save(ctx, validation(seq, byte(seq+10))); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := repository.DeleteOlderThanSeq(ctx, 5, 2)
	if err != nil || deleted != 2 {
		t.Fatalf("bounded delete = %d, %v; want 2, nil", deleted, err)
	}
	remainingOld := 0
	for seq := relationaldb.LedgerIndex(1); seq < 5; seq++ {
		rows, err := repository.GetValidationsForLedger(ctx, seq)
		if err != nil {
			t.Fatal(err)
		}
		remainingOld += len(rows)
	}
	if remainingOld != 2 {
		t.Fatalf("old rows after bounded delete = %d, want 2", remainingOld)
	}
	deleted, err = repository.DeleteOlderThanSeq(ctx, 5, 0)
	if err != nil || deleted != 2 {
		t.Fatalf("unbounded delete = %d, %v; want 2, nil", deleted, err)
	}
	rows, err = repository.GetValidationsForLedger(ctx, 5)
	if err != nil || len(rows) != 1 {
		t.Fatalf("delete boundary rows = %+v, %v; want one, nil", rows, err)
	}
}

func runAmendmentRepository(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	manager := factory(t)
	repository := manager.Amendment()

	rows, err := repository.LoadAmendmentVotes(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty load = %+v, %v; want empty, nil", rows, err)
	}
	firstID := strings.Repeat("A", 64)
	secondID := strings.Repeat("B", 64)
	first := relationaldb.AmendmentVoteRecord{Amendment: firstID, Name: "First", Vetoed: false}
	second := relationaldb.AmendmentVoteRecord{Amendment: secondID, Name: "Second", Vetoed: true}
	if err := repository.SaveAmendmentVote(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveAmendmentVote(ctx, second); err != nil {
		t.Fatal(err)
	}
	rows, err = repository.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	votes := amendmentVotesByID(rows)
	if len(votes) != 2 || votes[firstID] != first || votes[secondID] != second {
		t.Fatalf("loaded votes = %+v", votes)
	}

	first.Name = "Updated"
	first.Vetoed = true
	if err := repository.SaveAmendmentVote(ctx, first); err != nil {
		t.Fatal(err)
	}
	rows, err = repository.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	votes = amendmentVotesByID(rows)
	if len(votes) != 2 || votes[firstID] != first {
		t.Fatalf("upserted votes = %+v", votes)
	}
	if err := repository.DeleteAmendmentVote(ctx, secondID); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteAmendmentVote(ctx, strings.Repeat("C", 64)); err != nil {
		t.Fatalf("absent delete: %v", err)
	}
	rows, err = repository.LoadAmendmentVotes(ctx)
	if err != nil || len(rows) != 1 || *rows[0] != first {
		t.Fatalf("votes after delete = %+v, %v; want first only", rows, err)
	}

	for _, amendment := range []string{
		"",
		strings.Repeat("A", 63),
		strings.Repeat("A", 65),
		strings.Repeat("a", 64),
		strings.Repeat("G", 64),
	} {
		if err := repository.SaveAmendmentVote(ctx, relationaldb.AmendmentVoteRecord{
			Amendment: amendment,
			Name:      "invalid",
		}); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("malformed amendment save %q error = %v, want ErrInvalidData", amendment, err)
		}
		if err := repository.DeleteAmendmentVote(ctx, amendment); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("malformed amendment delete %q error = %v, want ErrInvalidData", amendment, err)
		}
	}
}

// WaitForTransactionRejection waits until a closing manager rejects new work.
func WaitForTransactionRejection(t *testing.T, manager relationaldb.RepositoryManager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := make(chan error, 1)
		go func() {
			result <- manager.WithTransaction(context.Background(), func(relationaldb.TransactionRepositories) error {
				return nil
			})
		}()
		select {
		case err := <-result:
			if errors.Is(err, relationaldb.ErrDatabaseClosed) {
				return
			}
			if err != nil {
				t.Fatalf("probing transaction: %v", err)
			}
		case <-time.After(time.Millisecond):
		}
	}
	t.Fatal("transactions were not rejected while Close was pending")
}

func ledger(seq byte) relationaldb.LedgerInfo {
	var result relationaldb.LedgerInfo
	result.Hash[0] = seq
	result.ParentHash[0] = seq - 1
	result.AccountHash[1] = seq
	result.TransactionHash[2] = seq
	result.Sequence = relationaldb.LedgerIndex(seq)
	result.TotalCoins = 100_000_000 + relationaldb.Amount(seq)
	result.CloseTime = time.Unix(1_700_000_000+int64(seq), 0).UTC()
	result.ParentCloseTime = result.CloseTime.Add(-time.Second)
	result.CloseTimeRes = 10
	result.CloseFlags = uint32(seq)
	return result
}

func transaction(seq byte) relationaldb.TransactionInfo {
	var result relationaldb.TransactionInfo
	result.Hash[0] = seq
	result.LedgerSeq = relationaldb.LedgerIndex(seq)
	result.TxnSeq = 1
	result.Status = "validated"
	result.RawTxn = []byte{1, 2, 3}
	result.TxnMeta = []byte{4, 5, 6}
	return result
}

func validatedLedger(seq byte) relationaldb.ValidatedLedger {
	var account relationaldb.AccountID
	account[0] = seq
	return relationaldb.ValidatedLedger{
		Ledger: ledger(seq),
		Transactions: []relationaldb.IndexedTransaction{{
			Transaction: transaction(seq),
			Accounts:    []relationaldb.AccountID{account},
		}},
	}
}

func transactionAt(hashByte byte, ledgerSeq relationaldb.LedgerIndex, txnSeq uint32) relationaldb.TransactionInfo {
	result := transaction(hashByte)
	result.LedgerSeq = ledgerSeq
	result.TxnSeq = txnSeq
	result.RawTxn = []byte{hashByte, byte(ledgerSeq), byte(txnSeq)}
	result.TxnMeta = []byte{byte(txnSeq), byte(ledgerSeq), hashByte}
	return result
}

func accountID(value byte) relationaldb.AccountID {
	var result relationaldb.AccountID
	result[0] = value
	result[len(result)-1] = value
	return result
}

func validation(ledgerSeq uint32, nodeByte byte) *relationaldb.ValidationRecord {
	result := &relationaldb.ValidationRecord{
		LedgerSeq:  relationaldb.LedgerIndex(ledgerSeq),
		InitialSeq: relationaldb.LedgerIndex(ledgerSeq - 1),
		NodePubKey: make([]byte, 33),
		SignTime:   time.Unix(1_700_000_000+int64(ledgerSeq), 0).UTC(),
		SeenTime:   time.Unix(1_700_000_100+int64(ledgerSeq), 0).UTC(),
		Flags:      ledgerSeq | 0x80000000,
		Raw:        []byte{nodeByte, byte(ledgerSeq), 0xaa},
	}
	result.LedgerHash[0] = byte(ledgerSeq)
	result.LedgerHash[len(result.LedgerHash)-1] = nodeByte
	result.NodePubKey[0] = 0x02
	result.NodePubKey[len(result.NodePubKey)-1] = nodeByte
	return result
}

func validationWithKeyWidth(ledgerSeq uint32, nodeByte byte, width int) *relationaldb.ValidationRecord {
	result := validation(ledgerSeq, nodeByte)
	result.NodePubKey = make([]byte, width)
	return result
}

func sameLedger(got, want *relationaldb.LedgerInfo) bool {
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

func sameStoredTransaction(got, want *relationaldb.TransactionInfo) bool {
	return got != nil && want != nil &&
		got.Hash == want.Hash &&
		got.LedgerSeq == want.LedgerSeq &&
		got.Status == want.Status &&
		bytes.Equal(got.RawTxn, want.RawTxn) &&
		bytes.Equal(got.TxnMeta, want.TxnMeta)
}

func sameValidation(got, want *relationaldb.ValidationRecord) bool {
	return got != nil && want != nil &&
		got.LedgerSeq == want.LedgerSeq &&
		got.InitialSeq == want.InitialSeq &&
		got.LedgerHash == want.LedgerHash &&
		bytes.Equal(got.NodePubKey, want.NodePubKey) &&
		got.SignTime.Equal(want.SignTime) &&
		got.SeenTime.Equal(want.SeenTime) &&
		got.Flags == want.Flags &&
		bytes.Equal(got.Raw, want.Raw)
}

func assertAccountPage(t *testing.T, page *relationaldb.AccountTxResult, options relationaldb.AccountTxPageOptions, want ...relationaldb.TransactionInfo) {
	t.Helper()
	if page == nil {
		t.Fatal("account transaction page is nil")
	}
	if page.LedgerRange.Min != options.MinLedger ||
		page.LedgerRange.Max != options.MaxLedger ||
		page.Limit != options.Limit {
		t.Fatalf("page metadata = %+v; want range %d-%d limit %d", page, options.MinLedger, options.MaxLedger, options.Limit)
	}
	if len(page.Transactions) != len(want) {
		t.Fatalf("page transactions = %+v; want %+v", page.Transactions, want)
	}
	for i := range want {
		got := &page.Transactions[i]
		if !sameStoredTransaction(got, &want[i]) ||
			got.TxnSeq != want[i].TxnSeq ||
			got.Account != options.Account {
			t.Fatalf("page transaction %d = %+v; want %+v for account %x", i, got, want[i], options.Account)
		}
	}
}

func assertMarker(t *testing.T, marker *relationaldb.AccountTxMarker, ledgerSeq relationaldb.LedgerIndex, txnSeq uint32) {
	t.Helper()
	if marker == nil || marker.LedgerSeq != ledgerSeq || marker.TxnSeq != txnSeq {
		t.Fatalf("marker = %+v; want ledger %d transaction %d", marker, ledgerSeq, txnSeq)
	}
}

func amendmentVotesByID(rows []*relationaldb.AmendmentVoteRecord) map[string]relationaldb.AmendmentVoteRecord {
	result := make(map[string]relationaldb.AmendmentVoteRecord, len(rows))
	for _, row := range rows {
		if row != nil {
			result[row.Amendment] = *row
		}
	}
	return result
}

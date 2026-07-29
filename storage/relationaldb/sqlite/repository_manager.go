package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
	_ "modernc.org/sqlite"
)

type RepositoryManager struct {
	ledgerDB *sqlutil.DB
	txDB     *sqlutil.DB
	gate     sqlutil.OperationGate

	ledgerRepo             *ledgerRepository
	transactionRepo        *transactionRepository
	accountTransactionRepo *accountTransactionRepository
	validationRepo         relationaldb.ValidationRepository
	amendmentVoteRepo      *amendmentVoteRepository

	persistMu   sync.Mutex
	persistHook func(stage string, index int) error
}

// Settings controls SQLite connection pragmas.
type Settings struct {
	JournalMode      string
	Synchronous      string
	TempStore        string
	PageSize         int
	JournalSizeLimit int
}

var _ relationaldb.RepositoryManager = (*RepositoryManager)(nil)

// NewRepositoryManager opens and migrates SQLite repositories in dbDir.
func NewRepositoryManager(ctx context.Context, dbDir string, settings Settings) (*RepositoryManager, error) {
	if dbDir == "" {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "database directory is required", nil)
	}
	normalized, err := settings.normalized()
	if err != nil {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "invalid SQLite settings", err)
	}
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return nil, relationaldb.NewConnectionError("new_repository_manager", "create database directory", err)
	}

	ledgerRaw, err := sql.Open("sqlite", filepath.Join(dbDir, "ledger.db"))
	if err != nil {
		return nil, relationaldb.NewConnectionError("new_repository_manager", "open ledger database", err)
	}
	txRaw, err := sql.Open("sqlite", filepath.Join(dbDir, "transaction.db"))
	if err != nil {
		_ = ledgerRaw.Close()
		return nil, relationaldb.NewConnectionError("new_repository_manager", "open transaction database", err)
	}
	cleanup := func() {
		_ = ledgerRaw.Close()
		_ = txRaw.Close()
	}
	for _, db := range []*sql.DB{ledgerRaw, txRaw} {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := applyPragmas(ctx, db, normalized); err != nil {
			cleanup()
			return nil, relationaldb.NewConnectionError("new_repository_manager", "apply SQLite pragmas", err)
		}
	}
	if err := migrate(ctx, ledgerRaw, ledgerMigrations); err != nil {
		cleanup()
		return nil, relationaldb.NewSchemaError("new_repository_manager", "migrate ledger database", err)
	}
	if err := migrate(ctx, txRaw, transactionMigrations); err != nil {
		cleanup()
		return nil, relationaldb.NewSchemaError("new_repository_manager", "migrate transaction database", err)
	}

	rm := &RepositoryManager{
		ledgerDB: sqlutil.NewDB(ledgerRaw),
		txDB:     sqlutil.NewDB(txRaw),
	}
	rm.ledgerRepo = newLedgerRepository(rm.ledgerDB)
	rm.transactionRepo = newTransactionRepository(rm.txDB)
	rm.accountTransactionRepo = newAccountTransactionRepository(rm.txDB)
	rm.validationRepo = sqlutil.NewGatedValidationRepository(&rm.gate, newValidationRepository(rm.ledgerDB))
	rm.amendmentVoteRepo = newAmendmentVoteRepository(rm.ledgerDB)
	return rm, nil
}

func (s Settings) normalized() (Settings, error) {
	s.JournalMode = strings.ToLower(defaultString(s.JournalMode, "wal"))
	s.Synchronous = strings.ToLower(defaultString(s.Synchronous, "normal"))
	s.TempStore = strings.ToLower(defaultString(s.TempStore, "file"))
	if !slices.Contains([]string{"delete", "truncate", "persist", "memory", "wal", "off"}, s.JournalMode) {
		return Settings{}, fmt.Errorf("invalid journal mode %q", s.JournalMode)
	}
	if !slices.Contains([]string{"off", "normal", "full", "extra"}, s.Synchronous) {
		return Settings{}, fmt.Errorf("invalid synchronous mode %q", s.Synchronous)
	}
	if !slices.Contains([]string{"default", "file", "memory"}, s.TempStore) {
		return Settings{}, fmt.Errorf("invalid temp store %q", s.TempStore)
	}
	if s.PageSize != 0 && (s.PageSize < 512 || s.PageSize > 65536 || s.PageSize&(s.PageSize-1) != 0) {
		return Settings{}, fmt.Errorf("page size must be a power of two between 512 and 65536")
	}
	if s.JournalSizeLimit < 0 {
		return Settings{}, fmt.Errorf("journal size limit must be non-negative")
	}
	return s, nil
}

func applyPragmas(ctx context.Context, db *sql.DB, settings Settings) error {
	statements := []string{}
	if settings.PageSize > 0 {
		statements = append(statements, fmt.Sprintf("PRAGMA page_size = %d", settings.PageSize))
	}
	statements = append(statements,
		"PRAGMA journal_mode = "+settings.JournalMode,
		"PRAGMA synchronous = "+settings.Synchronous,
		"PRAGMA cache_size = -64000",
		"PRAGMA temp_store = "+settings.TempStore,
		"PRAGMA foreign_keys = ON",
	)
	if settings.JournalSizeLimit > 0 {
		statements = append(statements, fmt.Sprintf("PRAGMA journal_size_limit = %d", settings.JournalSizeLimit))
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// Close waits for active operations and closes both databases.
func (rm *RepositoryManager) Close() error {
	if rm == nil {
		return nil
	}
	return rm.gate.Close(func() error {
		return errors.Join(rm.ledgerDB.Close(), rm.txDB.Close())
	})
}

// Ledger returns the ledger repository.
func (rm *RepositoryManager) Ledger() relationaldb.LedgerRepository {
	return rm.ledgerRepo
}

// Transaction returns the transaction repository.
func (rm *RepositoryManager) Transaction() relationaldb.TransactionRepository {
	return rm.transactionRepo
}

func (rm *RepositoryManager) AccountTransaction() relationaldb.AccountTransactionRepository {
	return rm.accountTransactionRepo
}

func (rm *RepositoryManager) Validation() relationaldb.ValidationRepository {
	return rm.validationRepo
}

func (rm *RepositoryManager) Amendment() relationaldb.AmendmentVoteRepository {
	return rm.amendmentVoteRepo
}

func (rm *RepositoryManager) WithTransaction(ctx context.Context, fn func(relationaldb.TransactionRepositories) error) (err error) {
	end, err := rm.gate.Begin()
	if err != nil {
		return err
	}
	defer end()
	return rm.withTransaction(ctx, fn)
}

func (rm *RepositoryManager) withTransaction(ctx context.Context, fn func(relationaldb.TransactionRepositories) error) (err error) {
	tx, err := rm.txDB.BeginTx(ctx, nil)
	if err != nil {
		return relationaldb.NewTransactionError("begin", "begin transaction", err)
	}
	scoped := newTransactionRepositories(tx)
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()
	if err := fn(scoped); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return relationaldb.NewTransactionError("commit", "commit transaction", err)
	}
	return nil
}

// PersistValidatedLedger stores complete indexes before publishing the header.
func (rm *RepositoryManager) PersistValidatedLedger(ctx context.Context, value relationaldb.ValidatedLedger) error {
	if err := value.Validate(); err != nil {
		return err
	}
	end, err := rm.gate.Begin()
	if err != nil {
		return err
	}
	defer end()
	rm.persistMu.Lock()
	defer rm.persistMu.Unlock()

	if err := rm.ledgerRepo.deleteLedgerBySequence(ctx, value.Ledger.Sequence); err != nil {
		return err
	}
	if err := rm.withTransaction(ctx, func(repos relationaldb.TransactionRepositories) error {
		scoped := repos.(*transactionRepositories)
		if err := scoped.accountTransaction.deleteByLedgerSequence(ctx, value.Ledger.Sequence); err != nil {
			return err
		}
		if err := repos.Transaction().DeleteTransactionsByLedgerSeq(ctx, value.Ledger.Sequence); err != nil {
			return err
		}
		index := 0
		for _, indexed := range value.Transactions {
			if err := scoped.accountTransaction.deleteByTransactionID(ctx, indexed.Transaction.Hash); err != nil {
				return err
			}
			if err := repos.Transaction().SaveTransaction(ctx, indexed.Transaction); err != nil {
				return err
			}
			index++
			if rm.persistHook != nil {
				if err := rm.persistHook("index", index); err != nil {
					return err
				}
			}
			for _, account := range indexed.Accounts {
				if err := repos.AccountTransaction().SaveAccountTransaction(ctx, account, indexed.Transaction); err != nil {
					return err
				}
				index++
				if rm.persistHook != nil {
					if err := rm.persistHook("index", index); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if rm.persistHook != nil {
		if err := rm.persistHook("ledger", len(value.Transactions)); err != nil {
			return err
		}
	}
	return rm.ledgerRepo.SaveValidatedLedger(ctx, value.Ledger)
}

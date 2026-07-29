package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
	_ "github.com/lib/pq"
)

// RepositoryManager owns PostgreSQL relational repositories and their lifecycle.
type RepositoryManager struct {
	db   *sqlutil.DB
	gate sqlutil.OperationGate

	ledgerRepo             *ledgerRepository
	transactionRepo        *transactionRepository
	accountTransactionRepo *accountTransactionRepository
	validationRepo         relationaldb.ValidationRepository
	amendmentVoteRepo      *amendmentVoteRepository

	persistHook func(stage string, index int) error
}

var _ relationaldb.RepositoryManager = (*RepositoryManager)(nil)

// NewRepositoryManager opens and migrates a PostgreSQL repository.
func NewRepositoryManager(ctx context.Context, config *relationaldb.Config) (*RepositoryManager, error) {
	if config == nil {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "configuration is required", nil)
	}
	if err := config.Validate(); err != nil {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "invalid configuration", err)
	}
	connStr, err := config.BuildConnectionString()
	if err != nil {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "build connection string", err)
	}
	raw, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, relationaldb.NewConnectionError("new_repository_manager", "open database", err)
	}
	raw.SetMaxOpenConns(config.MaxOpenConns)
	raw.SetMaxIdleConns(config.MaxIdleConns)
	raw.SetConnMaxLifetime(config.ConnMaxLifetime)
	raw.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	timeoutCtx, cancel := context.WithTimeout(ctx, config.DefaultTimeout)
	defer cancel()
	if err := raw.PingContext(timeoutCtx); err != nil {
		_ = raw.Close()
		return nil, relationaldb.NewConnectionError("new_repository_manager", "ping database", err)
	}
	if err := migrate(ctx, raw); err != nil {
		_ = raw.Close()
		return nil, relationaldb.NewSchemaError("new_repository_manager", "migrate database", err)
	}

	rm := &RepositoryManager{db: sqlutil.NewDB(raw)}
	rm.ledgerRepo = newLedgerRepository(rm.db)
	rm.transactionRepo = newTransactionRepository(rm.db)
	rm.accountTransactionRepo = newAccountTransactionRepository(rm.db)
	rm.validationRepo = sqlutil.NewGatedValidationRepository(&rm.gate, newValidationRepository(rm.db))
	rm.amendmentVoteRepo = newAmendmentVoteRepository(rm.db)
	return rm, nil
}

// Close waits for active operations and closes the database.
func (rm *RepositoryManager) Close() error {
	if rm == nil {
		return nil
	}
	return rm.gate.Close(rm.db.Close)
}

// Ledger returns the ledger repository.
func (rm *RepositoryManager) Ledger() relationaldb.LedgerRepository {
	return rm.ledgerRepo
}

// Transaction returns the transaction repository.
func (rm *RepositoryManager) Transaction() relationaldb.TransactionRepository {
	return rm.transactionRepo
}

// AccountTransaction returns the account transaction repository.
func (rm *RepositoryManager) AccountTransaction() relationaldb.AccountTransactionRepository {
	return rm.accountTransactionRepo
}

// Validation returns the validation archive repository.
func (rm *RepositoryManager) Validation() relationaldb.ValidationRepository {
	return rm.validationRepo
}

// Amendment returns the amendment vote repository.
func (rm *RepositoryManager) Amendment() relationaldb.AmendmentVoteRepository {
	return rm.amendmentVoteRepo
}

// WithTransaction invokes fn with transaction-bound repositories.
func (rm *RepositoryManager) WithTransaction(ctx context.Context, fn func(relationaldb.TransactionRepositories) error) (err error) {
	end, err := rm.gate.Begin()
	if err != nil {
		return err
	}
	defer end()
	return rm.withTransaction(ctx, fn)
}

func (rm *RepositoryManager) withTransaction(ctx context.Context, fn func(relationaldb.TransactionRepositories) error) (err error) {
	tx, err := rm.db.BeginTx(ctx, nil)
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

// PersistValidatedLedger atomically stores a validated ledger and its indexes.
func (rm *RepositoryManager) PersistValidatedLedger(ctx context.Context, value relationaldb.ValidatedLedger) (err error) {
	if err := value.Validate(); err != nil {
		return err
	}
	end, err := rm.gate.Begin()
	if err != nil {
		return err
	}
	defer end()
	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return relationaldb.NewTransactionError("persist_validated_ledger", "begin transaction", err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()
	transactionRepo := newTransactionRepository(tx)
	accountRepo := newAccountTransactionRepository(tx)
	ledgerRepo := newLedgerRepository(tx)
	if err := accountRepo.deleteByLedgerSequence(ctx, value.Ledger.Sequence); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := transactionRepo.DeleteTransactionsByLedgerSeq(ctx, value.Ledger.Sequence); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	index := 0
	for _, indexed := range value.Transactions {
		if err := accountRepo.deleteByTransactionID(ctx, indexed.Transaction.Hash); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := transactionRepo.SaveTransaction(ctx, indexed.Transaction); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		index++
		if rm.persistHook != nil {
			if err := rm.persistHook("index", index); err != nil {
				return errors.Join(err, tx.Rollback())
			}
		}
		for _, account := range indexed.Accounts {
			if err := accountRepo.SaveAccountTransaction(ctx, account, indexed.Transaction); err != nil {
				return errors.Join(err, tx.Rollback())
			}
			index++
			if rm.persistHook != nil {
				if err := rm.persistHook("index", index); err != nil {
					return errors.Join(err, tx.Rollback())
				}
			}
		}
	}
	if err := ledgerRepo.SaveValidatedLedger(ctx, value.Ledger); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if rm.persistHook != nil {
		if err := rm.persistHook("ledger", len(value.Transactions)); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return relationaldb.NewTransactionError("persist_validated_ledger", "commit transaction", err)
	}
	return nil
}

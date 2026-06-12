//go:build postgres

// These tests exercise the PostgreSQL backend against a real server and are
// gated behind the `postgres` build tag plus the XRPLD_TEST_POSTGRES_DSN
// environment variable, mirroring how the SQLite suite is structured (SQLite
// is embedded and needs no gating). Run with:
//
//	XRPLD_TEST_POSTGRES_DSN='postgres://user:pass@localhost:5432/xrpl_test?sslmode=disable' \
//	    go test -tags postgres ./storage/relationaldb/postgres/
package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Compile-time interface checks.
var (
	_ relationaldb.RepositoryManager            = (*RepositoryManager)(nil)
	_ relationaldb.LedgerRepository             = (*LedgerRepository)(nil)
	_ relationaldb.TransactionRepository        = (*TransactionRepository)(nil)
	_ relationaldb.AccountTransactionRepository = (*AccountTransactionRepository)(nil)
	_ relationaldb.SystemRepository             = (*SystemRepository)(nil)
	_ relationaldb.ValidationRepository         = (*ValidationRepository)(nil)
	_ relationaldb.AmendmentVoteRepository      = (*AmendmentVoteRepository)(nil)
	_ relationaldb.TransactionContext           = (*TransactionContext)(nil)
)

const postgresDSNEnv = "XRPLD_TEST_POSTGRES_DSN"

func setupTestDB(t *testing.T) *RepositoryManager {
	t.Helper()
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping PostgreSQL integration tests", postgresDSNEnv)
	}

	cfg := relationaldb.NewConfig().WithConnectionString(dsn)
	rm, err := NewRepositoryManager(cfg)
	if err != nil {
		t.Fatalf("new repository manager: %v", err)
	}
	if err := rm.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}

	truncateAll(t, rm)
	t.Cleanup(func() {
		truncateAll(t, rm)
		_ = rm.Close(context.Background())
	})
	return rm
}

// truncateAll clears every table so each test starts from a clean slate even
// though the suite shares one database.
func truncateAll(t *testing.T, rm *RepositoryManager) {
	t.Helper()
	_, err := rm.db.ExecContext(context.Background(),
		`TRUNCATE ledgers, transactions, account_transactions, validations, feature_votes`)
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}

func makeLedgerInfo(seq uint32) *relationaldb.LedgerInfo {
	var hash, parentHash, accountHash, txHash relationaldb.Hash
	hash[0] = byte(seq)
	parentHash[0] = byte(seq - 1)
	accountHash[1] = byte(seq)
	txHash[2] = byte(seq)
	return &relationaldb.LedgerInfo{
		Hash:            hash,
		Sequence:        relationaldb.LedgerIndex(seq),
		ParentHash:      parentHash,
		AccountHash:     accountHash,
		TransactionHash: txHash,
		TotalCoins:      100000000000,
		CloseTime:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ParentCloseTime: time.Date(2024, 12, 31, 23, 59, 56, 0, time.UTC),
		CloseTimeRes:    10,
		CloseFlags:      0,
	}
}

func TestPostgresOpenClosePing(t *testing.T) {
	rm := setupTestDB(t)
	if err := rm.System().Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestPostgresLedgerCRUD(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	// Empty state.
	minSeq, err := rm.Ledger().GetMinLedgerSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if minSeq != nil {
		t.Fatal("expected nil min seq on empty DB")
	}

	info := makeLedgerInfo(10)
	if err := rm.Ledger().SaveValidatedLedger(ctx, info, true); err != nil {
		t.Fatal(err)
	}

	got, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 10 {
		t.Fatalf("expected seq 10, got %d", got.Sequence)
	}
	if got.TotalCoins != 100000000000 {
		t.Fatalf("expected total_coins 100000000000, got %d", got.TotalCoins)
	}
	if got.Hash != info.Hash {
		t.Fatal("hash mismatch")
	}

	got2, err := rm.Ledger().GetLedgerInfoByHash(ctx, info.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Sequence != 10 {
		t.Fatalf("expected seq 10 from hash lookup, got %d", got2.Sequence)
	}

	newest, err := rm.Ledger().GetNewestLedgerInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newest.Sequence != 10 {
		t.Fatalf("expected newest seq 10, got %d", newest.Sequence)
	}

	stats, err := rm.Ledger().GetLedgerCountMinMax(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 1 {
		t.Fatalf("expected count 1, got %d", stats.Count)
	}

	// Upsert.
	info.TotalCoins = 200000000000
	if err := rm.Ledger().SaveValidatedLedger(ctx, info, true); err != nil {
		t.Fatal(err)
	}
	got3, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got3.TotalCoins != 200000000000 {
		t.Fatalf("expected upserted total_coins 200000000000, got %d", got3.TotalCoins)
	}
}

func TestPostgresLedgerHashQueries(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		if err := rm.Ledger().SaveValidatedLedger(ctx, makeLedgerInfo(i), false); err != nil {
			t.Fatal(err)
		}
	}

	hash, err := rm.Ledger().GetHashByIndex(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if hash[0] != 3 {
		t.Fatalf("expected hash[0]=3, got %d", hash[0])
	}

	pair, err := rm.Ledger().GetHashesByIndex(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if pair.LedgerHash[0] != 3 || pair.ParentHash[0] != 2 {
		t.Fatal("unexpected hash pair")
	}

	rangeResult, err := rm.Ledger().GetHashesByRange(ctx, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeResult) != 3 {
		t.Fatalf("expected 3 results, got %d", len(rangeResult))
	}
}

func TestPostgresLedgerDelete(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		if err := rm.Ledger().SaveValidatedLedger(ctx, makeLedgerInfo(i), false); err != nil {
			t.Fatal(err)
		}
	}

	if err := rm.Ledger().DeleteLedgersBySeq(ctx, 3); err != nil {
		t.Fatal(err)
	}
	stats, err := rm.Ledger().GetLedgerCountMinMax(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 2 {
		t.Fatalf("expected 2 remaining, got %d", stats.Count)
	}
}

func TestPostgresTransactionCRUD(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	txInfo := &relationaldb.TransactionInfo{
		LedgerSeq: 10,
		Status:    "validated",
		RawTxn:    []byte("raw-data"),
		TxnMeta:   []byte("meta-data"),
	}
	txInfo.Hash[0] = 0xAB

	if err := rm.Transaction().SaveTransaction(ctx, txInfo); err != nil {
		t.Fatal(err)
	}

	count, err := rm.Transaction().GetTransactionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	got, searchResult, err := rm.Transaction().GetTransaction(ctx, txInfo.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if searchResult != relationaldb.TxSearchAll {
		t.Fatalf("expected TxSearchAll, got %d", searchResult)
	}
	if got.LedgerSeq != 10 {
		t.Fatalf("expected ledger_seq 10, got %d", got.LedgerSeq)
	}
	if string(got.RawTxn) != "raw-data" {
		t.Fatal("raw_txn mismatch")
	}
	if string(got.TxnMeta) != "meta-data" {
		t.Fatal("txn_meta mismatch")
	}

	var missingHash relationaldb.Hash
	missingHash[0] = 0xFF
	_, sr, err := rm.Transaction().GetTransaction(ctx, missingHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sr != relationaldb.TxSearchUnknown {
		t.Fatalf("expected TxSearchUnknown, got %d", sr)
	}
}

func TestPostgresTransactionHistory(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: relationaldb.LedgerIndex(i),
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = byte(i)
		if err := rm.Transaction().SaveTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}

	history, err := rm.Transaction().GetTxHistory(ctx, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 results, got %d", len(history))
	}
	// Descending order.
	if history[0].LedgerSeq != 5 || history[2].LedgerSeq != 3 {
		t.Fatal("unexpected order")
	}
}

func TestPostgresAccountTransactionCRUD(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	var accountID relationaldb.AccountID
	accountID[0] = 0x01

	txInfo := &relationaldb.TransactionInfo{
		LedgerSeq: 10,
		TxnSeq:    1,
		Status:    "validated",
		RawTxn:    []byte("raw"),
	}
	txInfo.Hash[0] = 0xAA

	if err := rm.Transaction().SaveTransaction(ctx, txInfo); err != nil {
		t.Fatal(err)
	}
	if err := rm.AccountTransaction().SaveAccountTransaction(ctx, accountID, txInfo); err != nil {
		t.Fatal(err)
	}

	count, err := rm.AccountTransaction().GetAccountTransactionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}

	results, err := rm.AccountTransaction().GetOldestAccountTxs(ctx, relationaldb.AccountTxOptions{
		Account: accountID,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].LedgerSeq != 10 {
		t.Fatalf("expected ledger_seq 10, got %d", results[0].LedgerSeq)
	}
}

func TestPostgresAccountTransactionPagination(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	var accountID relationaldb.AccountID
	accountID[0] = 0x01

	for i := uint32(1); i <= 5; i++ {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: relationaldb.LedgerIndex(i),
			TxnSeq:    i,
			Status:    "validated",
			RawTxn:    []byte("raw"),
		}
		tx.Hash[0] = byte(i)
		if err := rm.Transaction().SaveTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if err := rm.AccountTransaction().SaveAccountTransaction(ctx, accountID, tx); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: accountID,
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Transactions) != 2 {
		t.Fatalf("expected 2 txs, got %d", len(page1.Transactions))
	}
	if page1.Marker == nil {
		t.Fatal("expected marker for more results")
	}

	page2, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: accountID,
		Limit:   2,
		Marker:  page1.Marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Transactions) != 2 {
		t.Fatalf("expected 2 txs, got %d", len(page2.Transactions))
	}

	page3, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: accountID,
		Limit:   2,
		Marker:  page2.Marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Transactions) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(page3.Transactions))
	}
	if page3.Marker != nil {
		t.Fatal("expected no marker on last page")
	}
}

func TestPostgresWithTransaction(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	// Commit path.
	err := rm.WithTransaction(ctx, func(tc relationaldb.TransactionContext) error {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: 1,
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = 0x01
		return tc.Transaction().SaveTransaction(ctx, tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	count, _ := rm.Transaction().GetTransactionCount(ctx)
	if count != 1 {
		t.Fatalf("expected 1 after commit, got %d", count)
	}

	// Rollback path.
	err = rm.WithTransaction(ctx, func(tc relationaldb.TransactionContext) error {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: 2,
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = 0x02
		if err := tc.Transaction().SaveTransaction(ctx, tx); err != nil {
			return err
		}
		return context.Canceled // force rollback
	})
	if err == nil {
		t.Fatal("expected error")
	}
	count, _ = rm.Transaction().GetTransactionCount(ctx)
	if count != 1 {
		t.Fatalf("expected 1 after rollback, got %d", count)
	}
}

func mkValidationRecord(ledgerSeq uint32, nodeByte byte) *relationaldb.ValidationRecord {
	rec := &relationaldb.ValidationRecord{
		LedgerSeq:  relationaldb.LedgerIndex(ledgerSeq),
		InitialSeq: relationaldb.LedgerIndex(ledgerSeq - 1),
		NodePubKey: make([]byte, 33),
		SignTime:   time.Unix(1700000000, 0).UTC(),
		SeenTime:   time.Unix(1700000005, 0).UTC(),
		Flags:      0x80000001,
		Raw:        []byte{0xDE, 0xAD, 0xBE, 0xEF, byte(ledgerSeq), nodeByte},
	}
	rec.LedgerHash[0] = byte(ledgerSeq)
	rec.LedgerHash[31] = nodeByte
	rec.NodePubKey[0] = 0x02
	rec.NodePubKey[32] = nodeByte
	return rec
}

func recordsEqual(a, b *relationaldb.ValidationRecord) bool {
	if a.LedgerSeq != b.LedgerSeq || a.InitialSeq != b.InitialSeq || a.Flags != b.Flags {
		return false
	}
	if a.LedgerHash != b.LedgerHash {
		return false
	}
	if !a.SignTime.Equal(b.SignTime) || !a.SeenTime.Equal(b.SeenTime) {
		return false
	}
	return byteEq(a.NodePubKey, b.NodePubKey) && byteEq(a.Raw, b.Raw)
}

func byteEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPostgresValidationRoundTrip(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()

	orig := mkValidationRecord(100, 0x01)
	if err := repo.Save(ctx, orig); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetValidationsForLedger(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if !recordsEqual(orig, got[0]) {
		t.Fatalf("roundtrip mismatch:\n got  %+v\n want %+v", got[0], orig)
	}

	// Duplicate save is a no-op.
	if err := repo.Save(ctx, orig); err != nil {
		t.Fatalf("duplicate save errored: %v", err)
	}
	count, err := repo.GetValidationCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after duplicate save, got %d", count)
	}
}

func TestPostgresValidationBatchAndSweep(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()

	// Distinct validators so all rows land.
	for seq := uint32(1); seq <= 20; seq++ {
		if err := repo.Save(ctx, mkValidationRecord(seq, byte(seq))); err != nil {
			t.Fatal(err)
		}
	}

	n, err := repo.DeleteOlderThanSeq(ctx, 15, 5)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("expected 5 deletions, got %d", n)
	}

	count, err := repo.GetValidationCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 15 {
		t.Fatalf("expected 15 rows remaining, got %d", count)
	}
}

func TestPostgresAmendmentVoteRoundTrip(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Amendment()

	got, err := repo.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no votes, got %d", len(got))
	}

	if err := repo.SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{Amendment: "AA", Name: "Alpha", Vetoed: false}); err != nil {
		t.Fatalf("save upvote: %v", err)
	}
	if err := repo.SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{Amendment: "BB", Name: "Beta", Vetoed: true}); err != nil {
		t.Fatalf("save veto: %v", err)
	}

	got, err = repo.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 votes, got %d", len(got))
	}
	byID := map[string]*relationaldb.AmendmentVoteRecord{}
	for _, r := range got {
		byID[r.Amendment] = r
	}
	if byID["AA"].Vetoed || byID["AA"].Name != "Alpha" {
		t.Fatalf("AA roundtrip wrong: %+v", byID["AA"])
	}
	if !byID["BB"].Vetoed {
		t.Fatalf("BB should be vetoed: %+v", byID["BB"])
	}

	// Upsert must not duplicate.
	if err := repo.SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{Amendment: "AA", Name: "Alpha", Vetoed: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = repo.LoadAmendmentVotes(ctx)
	if len(got) != 2 {
		t.Fatalf("upsert must not duplicate; got %d rows", len(got))
	}

	// Delete.
	if err := repo.DeleteAmendmentVote(ctx, "BB"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.LoadAmendmentVotes(ctx)
	if len(got) != 1 || got[0].Amendment != "AA" {
		t.Fatalf("after delete expected only AA, got %+v", got)
	}
}

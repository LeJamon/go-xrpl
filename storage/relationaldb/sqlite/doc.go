// Package sqlite implements the [github.com/LeJamon/goXRPLd/storage/relationaldb]
// repository interfaces on top of SQLite.
//
// It is the default relational backend and uses the pure-Go modernc.org/sqlite
// driver, so it needs no cgo. To match rippled's on-disk layout it keeps two
// database files: ledger.db (the Ledgers table) and transaction.db (the
// Transactions and AccountTransactions tables).
package sqlite

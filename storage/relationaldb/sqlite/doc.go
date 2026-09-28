// Package sqlite implements the [github.com/LeJamon/go-xrpl/storage/relationaldb]
// repository interfaces on top of SQLite.
//
// The default relational backend uses the pure-Go modernc.org/sqlite driver.
// Ledger headers, transactions, and account indexes share transaction.db so a
// validated ledger commits atomically. Validations and amendment votes remain
// in ledger.db. Schema version 6 imports legacy headers into transaction.db and
// removes indexes without a published header; the old header table is retired.
package sqlite

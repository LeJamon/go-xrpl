// Package relationaldb defines the SQL-backed storage layer for queryable ledger
// data.
//
// It declares the repository interfaces and shared value types (ledgers,
// transactions, the account-transaction index, validations, and amendment votes)
// that back account- and transaction-history RPC methods. This mirrors rippled's
// relational databases, which sit alongside the NodeStore: the NodeStore holds the
// content-addressed ledger state, while the relational layer provides the secondary
// indexes needed to answer "what transactions touched this account" style queries.
//
// The interfaces here are implemented by backend subpackages — see
// [github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite] (the default, matching
// rippled's ledger.db / transaction.db layout) and
// [github.com/LeJamon/go-xrpl/storage/relationaldb/postgres].
package relationaldb

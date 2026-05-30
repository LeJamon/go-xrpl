// Package postgres implements the [github.com/LeJamon/goXRPLd/storage/relationaldb]
// repository interfaces on top of PostgreSQL.
//
// It provides a RepositoryManager backed by a single PostgreSQL database (via the
// lib/pq driver) holding the ledger, transaction, account-transaction, validation,
// and amendment-vote repositories. It is an alternative to the default SQLite
// backend for deployments that want a shared, server-based relational store.
package postgres

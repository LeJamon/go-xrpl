package service

import (
	"context"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

type uncachedNodeReader interface {
	FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
}

type uncachedBatchNodeReader interface {
	FetchBatchUncached(context.Context, []nodestore.Hash256, int, int) ([]*nodestore.Node, error)
}

// nodeStoreFamily is a SHAMap family that persists flushed nodes into a node
// store and can report whether that store is a given database.
type nodeStoreFamily interface {
	shamap.Family
	UsesDatabase(nodestore.Database) bool
}

// nodeStoreCapabilities holds the optional node-store capabilities; an absent
// capability is nil.
type nodeStoreCapabilities struct {
	durable         nodestore.DurableDatabase
	durableSnapshot nodestore.DurableSnapshotDatabase
	generations     nodestore.GenerationDatabase
	uncached        uncachedNodeReader
	uncachedBatch   uncachedBatchNodeReader
}

// boundNodeStore is a node store whose optional capabilities were resolved
// once, when it was bound. A nil *boundNodeStore means no node store. It
// deliberately does not implement nodestore.Database: reach the store through
// database().
type boundNodeStore struct {
	db nodestore.Database
	nodeStoreCapabilities
}

func bindNodeStore(db nodestore.Database) *boundNodeStore {
	if db == nil {
		return nil
	}
	bound := &boundNodeStore{db: db}
	bound.durable, _ = db.(nodestore.DurableDatabase)
	bound.durableSnapshot, _ = db.(nodestore.DurableSnapshotDatabase)
	bound.generations, _ = db.(nodestore.GenerationDatabase)
	bound.uncached, _ = db.(uncachedNodeReader)
	bound.uncachedBatch, _ = db.(uncachedBatchNodeReader)
	return bound
}

// database returns the store, nil when there is none.
func (b *boundNodeStore) database() nodestore.Database {
	if b == nil {
		return nil
	}
	return b.db
}

func (b *boundNodeStore) capabilities() nodeStoreCapabilities {
	if b == nil {
		return nodeStoreCapabilities{}
	}
	return b.nodeStoreCapabilities
}

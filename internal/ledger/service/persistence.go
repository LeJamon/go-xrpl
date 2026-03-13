package service

import (
	"context"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// persistLedger writes the ledger state to storage backends
func (s *Service) persistLedger(l *ledger.Ledger) error {
	ctx := context.Background()
	seq := l.Sequence()

	// Persist to NodeStore if configured
	if s.nodeStore != nil {
		if err := s.persistToNodeStore(ctx, l, seq); err != nil {
			return err
		}
	}

	// Persist to RelationalDB if configured
	if s.relationalDB != nil {
		if err := s.persistToRelationalDB(ctx, l); err != nil {
			return err
		}
	}

	return nil
}

// persistToNodeStore writes ledger state to the nodestore.
// This stores both the SHAMap inner nodes and leaf nodes in prefix-serialized
// format so that NewFromRootHash() can reconstruct the tree via lazy loading.
func (s *Service) persistToNodeStore(ctx context.Context, l *ledger.Ledger, seq uint32) error {
	// Ensure the SHAMaps have a Family set so FlushDirty knows how to serialize
	family := s.getOrCreateFamily()

	l.SetStateMapFamily(family)
	l.SetTxMapFamily(family)

	// Flush all dirty SHAMap nodes (inner + leaf) in prefix-serialized format.
	// This is critical for recovery: NewFromRootHash() needs inner nodes in the
	// NodeStore for lazy loading to work.
	stateBatch, txBatch, err := l.FlushDirtyNodes()
	if err != nil {
		return fmt.Errorf("failed to flush dirty nodes: %w", err)
	}

	// Store state map nodes
	if len(stateBatch.Entries) > 0 {
		if err := family.StoreBatch(stateBatch.Entries); err != nil {
			return fmt.Errorf("failed to store state map nodes: %w", err)
		}
	}

	// Store transaction map nodes
	if len(txBatch.Entries) > 0 {
		if err := family.StoreBatch(txBatch.Entries); err != nil {
			return fmt.Errorf("failed to store tx map nodes: %w", err)
		}
	}

	// Persist ledger header
	headerData := l.SerializeHeader()
	headerNode := &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      nodestore.Hash256(l.Hash()),
		Data:      headerData,
		LedgerSeq: seq,
	}
	if err := s.nodeStore.Store(ctx, headerNode); err != nil {
		return fmt.Errorf("failed to store ledger header: %w", err)
	}

	// Sync to ensure durability
	return s.nodeStore.Sync()
}

// getOrCreateFamily returns the NodeStoreFamily for this service.
// It wraps the existing nodeStore database.
func (s *Service) getOrCreateFamily() *shamap.NodeStoreFamily {
	if s.family != nil {
		return s.family
	}
	s.family = shamap.NewNodeStoreFamily(s.nodeStore)
	return s.family
}

// persistToRelationalDB writes ledger metadata to the relational database
func (s *Service) persistToRelationalDB(ctx context.Context, l *ledger.Ledger) error {
	h := l.Header()

	// Get state and tx map hashes
	stateHash, _ := l.StateMapHash()
	txHash, _ := l.TxMapHash()

	// Create ledger info for storage
	ledgerInfo := &relationaldb.LedgerInfo{
		Hash:            relationaldb.Hash(l.Hash()),
		Sequence:        relationaldb.LedgerIndex(h.LedgerIndex),
		ParentHash:      relationaldb.Hash(h.ParentHash),
		AccountHash:     relationaldb.Hash(stateHash),
		TransactionHash: relationaldb.Hash(txHash),
		TotalCoins:      relationaldb.Amount(h.Drops),
		CloseTime:       h.CloseTime,
		ParentCloseTime: h.ParentCloseTime,
		CloseTimeRes:    int32(h.CloseTimeResolution),
		CloseFlags:      uint32(h.CloseFlags),
	}

	// Save validated ledger
	if err := s.relationalDB.Ledger().SaveValidatedLedger(ctx, ledgerInfo, true); err != nil {
		return err
	}

	return nil
}

package service

import (
	"context"

	"github.com/LeJamon/goXRPLd/internal/ledger"
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

// persistToNodeStore writes ledger state to the nodestore
func (s *Service) persistToNodeStore(ctx context.Context, l *ledger.Ledger, seq uint32) error {
	// Collect nodes to store in batch
	var nodes []*nodestore.Node

	// Persist state map entries
	err := l.ForEach(func(key [32]byte, data []byte) bool {
		node := &nodestore.Node{
			Type:      nodestore.NodeAccount,
			Hash:      nodestore.Hash256(key),
			Data:      data,
			LedgerSeq: seq,
		}
		nodes = append(nodes, node)
		return true
	})
	if err != nil {
		return err
	}

	// Store nodes in batch for efficiency
	if len(nodes) > 0 {
		if err := s.nodeStore.StoreBatch(ctx, nodes); err != nil {
			return err
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
		return err
	}

	// Sync to ensure durability
	return s.nodeStore.Sync()
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

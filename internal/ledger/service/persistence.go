package service

import (
	"context"
	"encoding/hex"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// persistLedger writes the ledger state to storage backends.
// The caller-supplied ctx is forwarded to every storage backend call so
// shutdown / request cancellation propagates through the persistence layer.
func (s *Service) persistLedger(ctx context.Context, l *ledger.Ledger) error {
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

// persistToRelationalDB writes ledger metadata and transactions to the relational database
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

	// Persist transactions to the relational DB for account_tx / tx_history queries
	seq := relationaldb.LedgerIndex(l.Sequence())

	l.ForEachTransaction(func(txHashBytes [32]byte, txData []byte) bool {
		txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(txData)
		if err != nil {
			s.logger.Warn("failed to split tx+meta blob", "tx", hex.EncodeToString(txHashBytes[:8]), "error", err)
			return true // skip this tx, continue
		}

		// Extract Account (sender) and optional Destination from the tx blob
		var accountID relationaldb.AccountID
		var destinationID relationaldb.AccountID

		txBlobHex := hex.EncodeToString(txBlob)
		txJSON, decErr := binarycodec.Decode(txBlobHex)
		if decErr == nil {
			if accountStr, ok := txJSON["Account"].(string); ok {
				if _, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(accountStr); err == nil && len(accountBytes) == 20 {
					copy(accountID[:], accountBytes)
				}
			}
			if destStr, ok := txJSON["Destination"].(string); ok {
				if _, destBytes, err := addresscodec.DecodeClassicAddressToAccountID(destStr); err == nil && len(destBytes) == 20 {
					copy(destinationID[:], destBytes)
				}
			}
		}

		// Extract TransactionIndex from metadata
		var txnSeq uint32
		if len(metaBlob) > 0 {
			metaHex := hex.EncodeToString(metaBlob)
			if metaJSON, err := binarycodec.Decode(metaHex); err == nil {
				if v, ok := metaJSON["TransactionIndex"].(float64); ok {
					txnSeq = uint32(v)
				}
			}
		}

		txInfo := &relationaldb.TransactionInfo{
			Hash:      relationaldb.Hash(txHashBytes),
			LedgerSeq: seq,
			TxnSeq:    txnSeq,
			Status:    "validated",
			RawTxn:    txBlob,
			TxnMeta:   metaBlob,
			Account:   accountID,
		}

		// Save to transactions table
		if err := s.relationalDB.Transaction().SaveTransaction(ctx, txInfo); err != nil {
			s.logger.Warn("failed to save transaction", "tx", hex.EncodeToString(txHashBytes[:8]), "error", err)
			return true
		}

		// Index for the sender account
		if !accountID.IsZero() {
			if err := s.relationalDB.AccountTransaction().SaveAccountTransaction(ctx, accountID, txInfo); err != nil {
				s.logger.Warn("failed to save account tx index", "tx", hex.EncodeToString(txHashBytes[:8]), "error", err)
			}
		}

		// Index for the destination account (if present and different from sender)
		if !destinationID.IsZero() && destinationID != accountID {
			if err := s.relationalDB.AccountTransaction().SaveAccountTransaction(ctx, destinationID, txInfo); err != nil {
				s.logger.Warn("failed to save destination tx index", "tx", hex.EncodeToString(txHashBytes[:8]), "error", err)
			}
		}

		return true // continue
	})

	return nil
}

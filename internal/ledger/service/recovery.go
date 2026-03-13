package service

import (
	"context"
	"fmt"
	"time"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// latestLedgerHash finds the hash of the latest persisted ledger.
// It first checks RelationalDB (if configured), then falls back to scanning
// the NodeStore for ledger header nodes.
// Returns the hash, whether a ledger was found, and any error.
func latestLedgerHash(ctx context.Context, relDB relationaldb.RepositoryManager, ns nodestore.Database) (hash [32]byte, found bool, err error) {
	// Try RelationalDB first — it has an index for fast lookup
	if relDB != nil {
		info, err := relDB.Ledger().GetNewestLedgerInfo(ctx)
		if err == nil && info != nil {
			return [32]byte(info.Hash), true, nil
		}
		// Not found or error — fall through to NodeStore
	}

	// Fall back to NodeStore: scan for NodeLedger entries
	// The NodeStore doesn't have an index by sequence, so we scan all entries
	// and pick the one with the highest sequence number.
	if ns != nil {
		var bestSeq uint32
		var bestHash [32]byte
		foundAny := false

		// Use ForEach on the backend if it's a KV database
		if kvDB, ok := ns.(*nodestore.KVDatabaseImpl); ok {
			_ = kvDB.ForEach(func(node *nodestore.Node) error {
				if node.Type == nodestore.NodeLedger {
					// Deserialize header to get sequence
					hdr, err := header.DeserializeHeader(node.Data, true)
					if err != nil {
						return nil // skip corrupt entries
					}
					if !foundAny || hdr.LedgerIndex > bestSeq {
						bestSeq = hdr.LedgerIndex
						bestHash = [32]byte(node.Hash)
						foundAny = true
					}
				}
				return nil
			})
		}

		if foundAny {
			return bestHash, true, nil
		}
	}

	return [32]byte{}, false, nil
}

// loadLedger reconstructs a Ledger from a persisted ledger header hash.
// It fetches the header from the NodeStore, deserializes it, then creates
// backed SHAMaps from the stored root hashes for lazy loading of state.
func (s *Service) loadLedger(ctx context.Context, ledgerHash [32]byte) (*ledger.Ledger, error) {
	if s.nodeStore == nil {
		return nil, fmt.Errorf("cannot load ledger: no NodeStore configured")
	}

	// 1. Fetch the ledger header node from NodeStore
	headerNode, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(ledgerHash))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ledger header: %w", err)
	}
	if headerNode == nil {
		return nil, fmt.Errorf("ledger header %x not found in NodeStore", ledgerHash[:8])
	}

	// 2. Deserialize the header
	hdr, err := header.DeserializeHeader(headerNode.Data, true)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize ledger header: %w", err)
	}

	// 3. Create a NodeStoreFamily from the existing nodeStore
	family := s.getOrCreateFamily()

	// 4. Reconstruct state SHAMap from the persisted root hash
	var stateMap *shamap.SHAMap
	if isZero(hdr.AccountHash) {
		stateMap, err = shamap.NewBacked(shamap.TypeState, family)
		if err != nil {
			return nil, fmt.Errorf("failed to create empty state map: %w", err)
		}
	} else {
		stateMap, err = shamap.NewFromRootHash(shamap.TypeState, hdr.AccountHash, family)
		if err != nil {
			return nil, fmt.Errorf("failed to reconstruct state map from hash %x: %w", hdr.AccountHash[:8], err)
		}
	}

	// 5. Reconstruct tx SHAMap from the persisted root hash.
	// An empty tx map (genesis or ledger with no transactions) has a zero hash.
	var txMap *shamap.SHAMap
	if isZero(hdr.TxHash) {
		txMap, err = shamap.New(shamap.TypeTransaction)
		if err != nil {
			return nil, fmt.Errorf("failed to create empty tx map: %w", err)
		}
	} else {
		txMap, err = shamap.NewFromRootHash(shamap.TypeTransaction, hdr.TxHash, family)
		if err != nil {
			return nil, fmt.Errorf("failed to reconstruct tx map from hash %x: %w", hdr.TxHash[:8], err)
		}
	}

	// 6. Mark maps as immutable (loaded ledger is validated/closed)
	if err := stateMap.SetImmutable(); err != nil {
		return nil, fmt.Errorf("failed to set state map immutable: %w", err)
	}
	if err := txMap.SetImmutable(); err != nil {
		return nil, fmt.Errorf("failed to set tx map immutable: %w", err)
	}

	// 7. Assemble the Ledger using the same pattern as FromGenesis
	loaded := ledger.FromGenesis(
		header.LedgerHeader{
			LedgerIndex:         hdr.LedgerIndex,
			ParentCloseTime:     hdr.ParentCloseTime,
			Hash:                hdr.Hash,
			TxHash:              hdr.TxHash,
			AccountHash:         hdr.AccountHash,
			ParentHash:          hdr.ParentHash,
			Drops:               hdr.Drops,
			Validated:           true,
			Accepted:            true,
			CloseFlags:          hdr.CloseFlags,
			CloseTimeResolution: hdr.CloseTimeResolution,
			CloseTime:           hdr.CloseTime,
		},
		stateMap,
		txMap,
		// Fees will be read from the FeeSettings SLE dynamically
		drops.Fees{},
	)

	return loaded, nil
}

// createGenesis creates a genesis ledger using the service config
func (s *Service) createGenesis() error {
	genesisResult, err := genesis.Create(s.config.GenesisConfig)
	if err != nil {
		return fmt.Errorf("failed to create genesis ledger: %w", err)
	}

	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		drops.Fees{},
	)

	s.genesisLedger = genesisLedger
	s.closedLedger = genesisLedger
	s.validatedLedger = genesisLedger
	s.ledgerHistory[genesisLedger.Sequence()] = genesisLedger

	// Create the first open ledger (ledger 2)
	openLedger, err := ledger.NewOpen(genesisLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger

	return nil
}

// isZero returns true if hash is all zeros.
func isZero(hash [32]byte) bool {
	for _, b := range hash {
		if b != 0 {
			return false
		}
	}
	return true
}

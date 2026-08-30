package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

const (
	fastLoadCheckpointVersion   = uint16(2)
	fastLoadCheckpointTombstone = byte(0)
	fastLoadCheckpointActive    = byte(1)
	fastLoadCheckpointFixedSize = 8 + 2 + 1 + 4 + 32 + 32 + 32 + 32 + 32 + 8 + 8 + 4 + 4 + 32

	fastLoadCheckpointIneligible  uint32 = 0
	fastLoadCheckpointEligible           = 1
	fastLoadCheckpointInvalidated        = 2
)

var (
	fastLoadCheckpointMagic = [8]byte{'g', 'o', 'X', 'R', 'P', 'L', 'f', 'c'}
	fastLoadCheckpointKey   = nodestore.Hash256(sha512half.Sum([]byte("go-xrpl fast-load checkpoint v1")))
)

type fastLoadCheckpoint struct {
	sequence             uint32
	ledgerHash           [32]byte
	stateRoot            [32]byte
	txRoot               [32]byte
	nodeStoreFingerprint [32]byte
	schemaFingerprint    [32]byte
	strictNodes          uint64
	strictElapsed        uint64
	stateProofs          [][32]byte
	txProofs             [][32]byte
}

func maxFastLoadProofs() int {
	total := 0
	level := 1
	for depth := 0; depth <= shamap.FullBelowCacheMaxDepth; depth++ {
		total += level
		level *= shamap.BranchFactor
	}
	return total
}

func maxFastLoadCheckpointSize() int {
	return fastLoadCheckpointFixedSize + 2*maxFastLoadProofs()*32
}

func encodeFastLoadCheckpoint(checkpoint *fastLoadCheckpoint) []byte {
	if checkpoint == nil {
		return finishFastLoadCheckpointEncoding(appendFastLoadCheckpointHeader(nil, fastLoadCheckpointTombstone))
	}
	data := appendFastLoadCheckpointHeader(
		make([]byte, 0, fastLoadCheckpointFixedSize+(len(checkpoint.stateProofs)+len(checkpoint.txProofs))*32),
		fastLoadCheckpointActive,
	)
	data = binary.BigEndian.AppendUint32(data, checkpoint.sequence)
	data = append(data, checkpoint.ledgerHash[:]...)
	data = append(data, checkpoint.stateRoot[:]...)
	data = append(data, checkpoint.txRoot[:]...)
	data = append(data, checkpoint.nodeStoreFingerprint[:]...)
	data = append(data, checkpoint.schemaFingerprint[:]...)
	data = binary.BigEndian.AppendUint64(data, checkpoint.strictNodes)
	data = binary.BigEndian.AppendUint64(data, checkpoint.strictElapsed)
	data = binary.BigEndian.AppendUint32(data, uint32(len(checkpoint.stateProofs)))
	data = binary.BigEndian.AppendUint32(data, uint32(len(checkpoint.txProofs)))
	for _, hash := range checkpoint.stateProofs {
		data = append(data, hash[:]...)
	}
	for _, hash := range checkpoint.txProofs {
		data = append(data, hash[:]...)
	}
	return finishFastLoadCheckpointEncoding(data)
}

func appendFastLoadCheckpointHeader(data []byte, status byte) []byte {
	data = append(data, fastLoadCheckpointMagic[:]...)
	data = binary.BigEndian.AppendUint16(data, fastLoadCheckpointVersion)
	return append(data, status)
}

func finishFastLoadCheckpointEncoding(data []byte) []byte {
	checksum := sha512half.Sum(data)
	return append(data, checksum[:]...)
}

func decodeFastLoadCheckpoint(data []byte) (*fastLoadCheckpoint, bool, error) {
	if len(data) > maxFastLoadCheckpointSize() {
		return nil, false, fmt.Errorf("checkpoint is oversized: %d bytes", len(data))
	}
	const headerSize = 8 + 2 + 1
	if len(data) < headerSize+32 {
		return nil, false, errors.New("checkpoint is truncated")
	}
	payload := data[:len(data)-32]
	wantChecksum := sha512half.Sum(payload)
	if !bytes.Equal(data[len(data)-32:], wantChecksum[:]) {
		return nil, false, errors.New("checkpoint checksum mismatch")
	}
	if !bytes.Equal(payload[:8], fastLoadCheckpointMagic[:]) {
		return nil, false, errors.New("checkpoint magic mismatch")
	}
	if version := binary.BigEndian.Uint16(payload[8:10]); version != fastLoadCheckpointVersion {
		return nil, false, fmt.Errorf("unsupported checkpoint version %d", version)
	}
	status := payload[10]
	if status == fastLoadCheckpointTombstone {
		if len(payload) != headerSize {
			return nil, false, errors.New("malformed checkpoint tombstone")
		}
		return nil, true, nil
	}
	if status != fastLoadCheckpointActive {
		return nil, false, fmt.Errorf("unknown checkpoint status %d", status)
	}
	if len(data) < fastLoadCheckpointFixedSize {
		return nil, false, errors.New("active checkpoint is truncated")
	}
	offset := headerSize
	checkpoint := &fastLoadCheckpoint{sequence: binary.BigEndian.Uint32(payload[offset : offset+4])}
	offset += 4
	copy(checkpoint.ledgerHash[:], payload[offset:offset+32])
	offset += 32
	copy(checkpoint.stateRoot[:], payload[offset:offset+32])
	offset += 32
	copy(checkpoint.txRoot[:], payload[offset:offset+32])
	offset += 32
	copy(checkpoint.nodeStoreFingerprint[:], payload[offset:offset+32])
	offset += 32
	copy(checkpoint.schemaFingerprint[:], payload[offset:offset+32])
	offset += 32
	checkpoint.strictNodes = binary.BigEndian.Uint64(payload[offset : offset+8])
	offset += 8
	checkpoint.strictElapsed = binary.BigEndian.Uint64(payload[offset : offset+8])
	offset += 8
	if checkpoint.strictElapsed > math.MaxInt64 {
		return nil, false, errors.New("checkpoint strict traversal duration overflows")
	}
	stateCount := binary.BigEndian.Uint32(payload[offset : offset+4])
	offset += 4
	txCount := binary.BigEndian.Uint32(payload[offset : offset+4])
	offset += 4
	maxProofs := uint32(maxFastLoadProofs())
	if stateCount == 0 || stateCount > maxProofs || txCount > maxProofs {
		return nil, false, fmt.Errorf("invalid checkpoint proof counts state=%d transaction=%d", stateCount, txCount)
	}
	proofBytes := uint64(stateCount+txCount) * 32
	if proofBytes != uint64(len(payload)-offset) {
		return nil, false, errors.New("checkpoint proof data length mismatch")
	}
	checkpoint.stateProofs = make([][32]byte, stateCount)
	checkpoint.txProofs = make([][32]byte, txCount)
	seen := make(map[[32]byte]struct{}, int(stateCount+txCount))
	readProofs := func(proofs [][32]byte) error {
		for i := range proofs {
			copy(proofs[i][:], payload[offset:offset+32])
			offset += 32
			if proofs[i] == ([32]byte{}) {
				return errors.New("checkpoint contains a zero proof hash")
			}
			if _, duplicate := seen[proofs[i]]; duplicate {
				return fmt.Errorf("checkpoint contains duplicate proof hash %x", proofs[i][:8])
			}
			seen[proofs[i]] = struct{}{}
		}
		return nil
	}
	if err := readProofs(checkpoint.stateProofs); err != nil {
		return nil, false, err
	}
	if err := readProofs(checkpoint.txProofs); err != nil {
		return nil, false, err
	}
	if checkpoint.sequence == 0 || checkpoint.ledgerHash == ([32]byte{}) || checkpoint.stateRoot == ([32]byte{}) ||
		checkpoint.nodeStoreFingerprint == ([32]byte{}) || checkpoint.schemaFingerprint == ([32]byte{}) {
		return nil, false, errors.New("checkpoint contains empty ledger identity")
	}
	if checkpoint.stateProofs[0] != checkpoint.stateRoot {
		return nil, false, errors.New("checkpoint state proof does not start at its root")
	}
	if checkpoint.txRoot == ([32]byte{}) {
		if len(checkpoint.txProofs) != 0 {
			return nil, false, errors.New("zero transaction root has proof data")
		}
	} else if len(checkpoint.txProofs) == 0 || checkpoint.txProofs[0] != checkpoint.txRoot {
		return nil, false, errors.New("checkpoint transaction proof does not start at its root")
	}
	return checkpoint, false, nil
}

func (s *Service) consumeFastLoadCheckpoint(ctx context.Context) (*fastLoadCheckpoint, error) {
	if s.nodeStore == nil {
		return nil, nil
	}
	node, fetchErr := s.nodeStore.Fetch(ctx, fastLoadCheckpointKey)
	if node == nil && fetchErr == nil {
		s.logger.Info("Fast-load checkpoint unavailable", "reason", "missing")
		return nil, nil
	}
	var checkpoint *fastLoadCheckpoint
	var tombstone bool
	var decodeErr error
	if fetchErr == nil {
		if node.Type != nodestore.NodeLedger || node.Hash != fastLoadCheckpointKey {
			decodeErr = errors.New("checkpoint has invalid NodeStore metadata")
		} else {
			checkpoint, tombstone, decodeErr = decodeFastLoadCheckpoint(node.Data)
			if decodeErr == nil && checkpoint != nil && node.LedgerSeq != checkpoint.sequence {
				decodeErr = errors.New("checkpoint sequence does not match NodeStore metadata")
			}
		}
	}
	if fetchErr == nil && decodeErr == nil && tombstone {
		s.logger.Info("Fast-load checkpoint unavailable", "reason", "tombstone")
		return nil, nil
	}
	durable, ok := s.nodeStore.(nodestore.DurableDatabase)
	if !ok {
		return nil, errors.New("consume fast-load checkpoint: NodeStore has no durable checkpoint capability")
	}
	tombstoneData := encodeFastLoadCheckpoint(nil)
	if err := durable.StoreDurable(context.WithoutCancel(ctx), &nodestore.Node{
		Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey, Data: tombstoneData,
	}); err != nil {
		return nil, fmt.Errorf("consume fast-load checkpoint: %w", err)
	}
	if fetchErr != nil {
		s.logger.Warn("Fast-load checkpoint unreadable and consumed", "err", fetchErr)
		return nil, nil
	}
	if decodeErr != nil {
		s.logger.Warn("Fast-load checkpoint rejected and consumed", "err", decodeErr)
		return nil, nil
	}
	s.logger.Info("Fast-load checkpoint consumed", "sequence", checkpoint.sequence)
	return checkpoint, nil
}

func (s *Service) collectFastLoadProofs(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
) ([][32]byte, error) {
	if root == ([32]byte{}) {
		if mapType == shamap.TypeTransaction {
			return nil, nil
		}
		return nil, errors.New("zero state root")
	}
	queue := []storedSHAMapNode{{hash: root}}
	proofs := make([][32]byte, 0, min(maxFastLoadProofs(), 256))
	seen := make(map[[32]byte]struct{})
	fetch := s.storedSHAMapVerificationFetch()
	for len(queue) != 0 {
		pending := queue[0]
		queue = queue[1:]
		if _, duplicate := seen[pending.hash]; duplicate {
			return nil, fmt.Errorf("reachable shallow graph contains duplicate node %x", pending.hash[:8])
		}
		seen[pending.hash] = struct{}{}
		node, _, err := s.loadStoredSHAMapNodeWithFetch(ctx, pending, mapType, fetch)
		if err != nil {
			return nil, err
		}
		proofs = append(proofs, pending.hash)
		if pending.depth == shamap.FullBelowCacheMaxDepth {
			continue
		}
		inner, ok := node.(shamap.InnerNodeReader)
		if !ok {
			continue
		}
		for branch := range shamap.BranchFactor {
			if inner.IsEmptyBranch(branch) {
				continue
			}
			child, err := inner.ChildHash(branch)
			if err != nil {
				return nil, err
			}
			queue = append(queue, storedSHAMapNode{hash: child, depth: pending.depth + 1})
		}
	}
	return proofs, nil
}

func (s *Service) acceptFastLoadCheckpoint(
	ctx context.Context,
	checkpoint *fastLoadCheckpoint,
	h header.LedgerHeader,
) (bool, error) {
	if checkpoint == nil {
		return false, nil
	}
	if checkpoint.sequence != h.LedgerIndex || checkpoint.ledgerHash != h.Hash ||
		checkpoint.stateRoot != h.AccountHash || checkpoint.txRoot != h.TxHash {
		return false, errors.New("checkpoint ledger identity does not match durable tip")
	}
	durable, schema, err := s.fastLoadDurability()
	if err != nil {
		return false, err
	}
	accepted := false
	err = durable.WithDurableSnapshot(ctx, func(nodeFingerprint [32]byte) error {
		if nodeFingerprint != checkpoint.nodeStoreFingerprint {
			return errors.New("checkpoint NodeStore identity or mutation generation mismatch")
		}
		schemaFingerprint, err := schema.SchemaFingerprint(ctx)
		if err != nil {
			return fmt.Errorf("fingerprint relational database: %w", err)
		}
		if schemaFingerprint != checkpoint.schemaFingerprint {
			return errors.New("checkpoint relational identity or schema mismatch")
		}
		stateProofs, err := s.collectFastLoadProofs(ctx, h.AccountHash, shamap.TypeState)
		if err != nil {
			return fmt.Errorf("revalidate checkpoint state proof: %w", err)
		}
		if !equalFastLoadProofs(stateProofs, checkpoint.stateProofs) {
			return errors.New("checkpoint state proof mismatch")
		}
		txProofs, err := s.collectFastLoadProofs(ctx, h.TxHash, shamap.TypeTransaction)
		if err != nil {
			return fmt.Errorf("revalidate checkpoint transaction proof: %w", err)
		}
		if !equalFastLoadProofs(txProofs, checkpoint.txProofs) {
			return errors.New("checkpoint transaction proof mismatch")
		}
		provider, ok := s.shamapFamily.(interface {
			FullBelowCache() *shamap.FullBelowCache
		})
		if !ok || provider.FullBelowCache() == nil {
			return errors.New("SHAMap family has no shared full-below cache")
		}
		cache := provider.FullBelowCache()
		generation := cache.Generation()
		for _, hash := range stateProofs {
			cache.Insert(generation, hash)
		}
		for _, hash := range txProofs {
			cache.Insert(generation, hash)
		}
		s.fastLoadStrictNodes.Store(checkpoint.strictNodes)
		s.fastLoadStrictElapsed.Store(checkpoint.strictElapsed)
		s.fastLoadBaseStateRoot = h.AccountHash
		s.fastLoadBaseFingerprint = nodeFingerprint
		s.fastLoadBaseVerified = true
		s.markFastLoadCheckpointEligible()
		s.logger.Info("Fast-load checkpoint accepted",
			"sequence", h.LedgerIndex,
			"strict_traversals_saved", 1+boolInt(h.TxHash != ([32]byte{})),
			"strict_nodes_saved", checkpoint.strictNodes,
			"strict_elapsed_saved", time.Duration(checkpoint.strictElapsed).String(),
			"shallow_nodes_read", len(stateProofs)+len(txProofs),
		)
		accepted = true
		return nil
	})
	return accepted, err
}

func equalFastLoadProofs(left, right [][32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// InvalidateFastLoadCheckpointEligibility prevents shutdown from publishing a
// proof across a destructive NodeStore mutation.
func (s *Service) InvalidateFastLoadCheckpointEligibility() {
	s.fastLoadCheckpointState.Store(fastLoadCheckpointInvalidated)
}

func (s *Service) markFastLoadCheckpointEligible() {
	s.fastLoadCheckpointState.CompareAndSwap(fastLoadCheckpointIneligible, fastLoadCheckpointEligible)
}

// AcquireFastLoadStateBase pins the accepted checkpoint's durable NodeStore
// generation for a frozen-pivot recovery session.
func (s *Service) AcquireFastLoadStateBase(ctx context.Context) ([32]byte, func(), bool, error) {
	s.mu.RLock()
	root := s.fastLoadBaseStateRoot
	expected := s.fastLoadBaseFingerprint
	eligible := s.networkLedgerState == networkLedgerFastLoadProvisional && s.fastLoadBaseVerified
	s.mu.RUnlock()
	if !eligible {
		return [32]byte{}, nil, false, nil
	}
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return [32]byte{}, nil, false, errors.New("NodeStore cannot retain a durable snapshot")
	}
	fingerprint, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		return [32]byte{}, nil, false, fmt.Errorf("acquire checkpoint NodeStore snapshot: %w", err)
	}
	if fingerprint != expected {
		release()
		return [32]byte{}, nil, false, errors.New("checkpoint NodeStore mutation generation changed before pivot acquisition")
	}
	s.mu.RLock()
	current := s.networkLedgerState == networkLedgerFastLoadProvisional && s.fastLoadBaseVerified &&
		s.fastLoadBaseStateRoot == root && s.fastLoadBaseFingerprint == expected
	s.mu.RUnlock()
	if !current {
		release()
		return [32]byte{}, nil, false, nil
	}
	return root, release, true, nil
}

func (s *Service) clearFastLoadBaseLocked() {
	s.fastLoadBaseStateRoot = [32]byte{}
	s.fastLoadBaseFingerprint = [32]byte{}
	s.fastLoadBaseVerified = false
}

// PrepareFastLoadCheckpoint publishes a one-use checkpoint for the next clean
// start. The service must already be stopped so persistence and validation tips
// cannot move while the durable state is checked.
func (s *Service) PrepareFastLoadCheckpoint(ctx context.Context) (bool, error) {
	if !s.config.FastLoad || s.nodeStore == nil || s.shamapFamily == nil || s.relationalDB == nil {
		s.logger.Info("Fast-load checkpoint not prepared", "reason", "checkpointing is not configured")
		return false, nil
	}
	s.lifecycleMu.Lock()
	stopped := s.lifecycleState == serviceStopped
	s.lifecycleMu.Unlock()
	if !stopped {
		return false, errors.New("prepare fast-load checkpoint before ledger service stopped")
	}
	if s.fastLoadCheckpointState.Load() != fastLoadCheckpointEligible {
		s.logger.Info("Fast-load checkpoint not prepared", "reason", "run is not eligible")
		return false, nil
	}
	if err := s.nodeStore.Sync(ctx); err != nil {
		return false, fmt.Errorf("sync NodeStore before fast-load checkpoint: %w", err)
	}
	s.mu.RLock()
	validated := s.validatedLedger
	s.mu.RUnlock()
	if validated == nil || !validated.IsValidated() || !s.hasCompleteLedger(validated) {
		return false, errors.New("latest validated ledger is not durably complete")
	}
	h := validated.Header()
	info, err := s.relationalDB.Ledger().GetNewestLedgerInfo(ctx)
	if err != nil {
		return false, fmt.Errorf("load newest relational ledger for checkpoint: %w", err)
	}
	tip, err := s.nodeStore.Fetch(ctx, validatedTipKey)
	if err != nil {
		return false, fmt.Errorf("fetch durable validated tip for checkpoint: %w", err)
	}
	if tip == nil || tip.Type != nodestore.NodeLedger || tip.LedgerSeq != h.LedgerIndex ||
		len(tip.Data) != 32 || !bytes.Equal(tip.Data, h.Hash[:]) {
		return false, errors.New("durable validated tip does not match latest validated ledger")
	}
	storedHeader, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(h.Hash))
	if err != nil {
		return false, fmt.Errorf("fetch durable ledger header for checkpoint: %w", err)
	}
	if storedHeader == nil || storedHeader.Type != nodestore.NodeLedger ||
		storedHeader.LedgerSeq != h.LedgerIndex || !bytes.Equal(storedHeader.Data, validated.SerializeHeader()) {
		return false, errors.New("durable ledger header does not match latest validated ledger")
	}
	durableHeader, err := header.DeserializeHeader(storedHeader.Data, true)
	if err != nil || durableHeader == nil {
		return false, errors.New("durable validated header is malformed")
	}
	if !storedHeaderMatchesInfo(*durableHeader, info) {
		return false, errors.New("newest relational ledger does not match durable validated header")
	}
	h = *durableHeader
	durable, schema, err := s.fastLoadDurability()
	if err != nil {
		s.logger.Info("Fast-load checkpoint not prepared", "reason", "durable fingerprint unavailable", "err", err)
		return false, nil
	}
	prepared := false
	err = durable.WithDurableSnapshot(ctx, func(nodeFingerprint [32]byte) error {
		if s.fastLoadCheckpointState.Load() != fastLoadCheckpointEligible {
			s.logger.Info("Fast-load checkpoint not prepared", "reason", "run was invalidated during preparation")
			return nil
		}
		schemaFingerprint, err := schema.SchemaFingerprint(ctx)
		if err != nil {
			return fmt.Errorf("fingerprint relational database: %w", err)
		}
		stateProofs, err := s.collectFastLoadProofs(ctx, h.AccountHash, shamap.TypeState)
		if err != nil {
			return fmt.Errorf("collect checkpoint state proof: %w", err)
		}
		txProofs, err := s.collectFastLoadProofs(ctx, h.TxHash, shamap.TypeTransaction)
		if err != nil {
			return fmt.Errorf("collect checkpoint transaction proof: %w", err)
		}
		if s.fastLoadCheckpointState.Load() != fastLoadCheckpointEligible {
			s.logger.Info("Fast-load checkpoint not prepared", "reason", "run was invalidated during preparation")
			return nil
		}
		currentSchemaFingerprint, err := schema.SchemaFingerprint(ctx)
		if err != nil {
			return fmt.Errorf("recheck relational fingerprint: %w", err)
		}
		if currentSchemaFingerprint != schemaFingerprint {
			return errors.New("relational identity changed during checkpoint preparation")
		}
		checkpoint := &fastLoadCheckpoint{
			sequence: h.LedgerIndex, ledgerHash: h.Hash,
			stateRoot: h.AccountHash, txRoot: h.TxHash,
			nodeStoreFingerprint: nodeFingerprint, schemaFingerprint: schemaFingerprint,
			strictNodes: s.fastLoadStrictNodes.Load(), strictElapsed: s.fastLoadStrictElapsed.Load(),
			stateProofs: stateProofs, txProofs: txProofs,
		}
		if err := durable.StoreDurable(ctx, &nodestore.Node{
			Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey,
			Data: encodeFastLoadCheckpoint(checkpoint), LedgerSeq: h.LedgerIndex,
		}); err != nil {
			return fmt.Errorf("durably store fast-load checkpoint: %w", err)
		}
		s.logger.Info("Fast-load checkpoint prepared",
			"sequence", h.LedgerIndex,
			"state_proofs", len(stateProofs),
			"transaction_proofs", len(txProofs),
			"shallow_nodes", len(stateProofs)+len(txProofs),
			"strict_nodes", checkpoint.strictNodes,
			"strict_elapsed", time.Duration(checkpoint.strictElapsed).String(),
		)
		prepared = true
		return nil
	})
	return prepared, err
}

func (s *Service) fastLoadDurability() (nodestore.DurableDatabase, relationaldb.SchemaFingerprinter, error) {
	durable, ok := s.nodeStore.(nodestore.DurableDatabase)
	if !ok {
		return nil, nil, errors.New("NodeStore has no durable checkpoint capability")
	}
	schema, ok := s.relationalDB.(relationaldb.SchemaFingerprinter)
	if !ok {
		return nil, nil, errors.New("relational database has no schema fingerprint capability")
	}
	return durable, schema, nil
}

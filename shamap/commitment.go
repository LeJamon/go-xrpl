package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Change is one final object change for an incremental commitment. Changes
// must be presented in strictly increasing key order. A deletion ignores Type
// and carries no Data; a non-deletion with Type zero uses the map's default
// leaf type.
type Change struct {
	Key    [32]byte
	Data   []byte
	Delete bool
	Type   NodeType
}

// ChangeIterator yields one sorted, unique Change at a time. It must return
// io.EOF after the final change. The builder invokes it synchronously and does
// not retain the returned Change or its Data after processing it.
type ChangeIterator func() (Change, error)

// ChangeSink receives one canonical node. Entry.Data is borrowed until the
// callback returns; a sink retaining it must copy it before returning. The
// builder emits nodes post-order, so every emitted inner node references a
// child hash already emitted or retained by the parent root.
type ChangeSink func(FlushEntry) error

// BuildResult describes the nodes emitted by BuildChanges. Nodes, Leaves and
// Bytes count emitted changed nodes only; they do not describe the total tree.
// MaxFrontier is the largest number of affected path frames active in the
// bounded traversal. Decoded frame payloads are released after branch
// metadata is copied, before recursive descent.
type BuildResult struct {
	Root        [32]byte
	Nodes       uint64
	Leaves      uint64
	Bytes       uint64
	MaxFrontier uint64
}

// CommitmentBudget is an optional shared byte budget for root reads and
// incremental commitment. TryReserve must be non-blocking. Changed returns a
// channel that is notified when another reservation may succeed.
type CommitmentBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
	Changed() <-chan struct{}
}

// CommitmentOption configures the optional workspace accounting used by a
// root reader or incremental builder.
type CommitmentOption func(*commitmentOptions)

type commitmentOptions struct {
	budget CommitmentBudget
	flush  func() error
}

func normalizeCommitmentContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// WithCommitmentBudget accounts decoded frontier and serialization workspace
// against budget. A failed reservation waits for Changed or ctx cancellation.
func WithCommitmentBudget(budget CommitmentBudget) CommitmentOption {
	return func(options *commitmentOptions) {
		options.budget = budget
	}
}

// WithCommitmentFlush supplies an optional pressure callback for incremental
// builders that retain emitted bytes outside BuildChanges. The callback runs
// once after a builder reservation is denied, before the normal Changed wait.
// It must release any retained budget before returning so the reservation can
// make progress; a returned error aborts the build.
func WithCommitmentFlush(flush func() error) CommitmentOption {
	return func(options *commitmentOptions) {
		options.flush = flush
	}
}

// ErrCommitmentBudget reports a valid node or payload that cannot fit in the
// configured shared workspace budget.
var ErrCommitmentBudget = errors.New("shamap: commitment workspace exceeds budget capacity")

type commitmentBudgetCapacity interface {
	Capacity() uint64
}

type commitmentBudgetLimit interface {
	Limit() uint64
}

func commitmentCapacity(budget CommitmentBudget) (uint64, bool) {
	if budget == nil {
		return 0, false
	}
	if capacity, ok := budget.(commitmentBudgetCapacity); ok {
		return capacity.Capacity(), true
	}
	if limit, ok := budget.(commitmentBudgetLimit); ok {
		return limit.Limit(), true
	}
	return 0, false
}

func reserveCommitmentMemory(ctx context.Context, budget CommitmentBudget, bytes uint64) error {
	return reserveCommitmentMemoryWithFlush(ctx, budget, bytes, nil)
}

func reserveCommitmentMemoryWithFlush(ctx context.Context, budget CommitmentBudget, bytes uint64, flush func() error) error {
	if bytes == 0 || budget == nil {
		return nil
	}
	if capacity, ok := commitmentCapacity(budget); ok && bytes > capacity {
		return fmt.Errorf("%w: need %d bytes, capacity %d", ErrCommitmentBudget, bytes, capacity)
	}
	flushed := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if budget.TryReserve(bytes) {
			return nil
		}
		if flush != nil && !flushed {
			flushed = true
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := flush(); err != nil {
				return err
			}
		}
		wake := budget.Changed()
		if budget.TryReserve(bytes) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func releaseCommitmentMemory(budget CommitmentBudget, bytes uint64) {
	if budget != nil && bytes != 0 {
		budget.Release(bytes)
	}
}

const commitmentNodeOverhead = uint64(1024)

func commitmentNodeCharge(data []byte) uint64 {
	return commitmentNodeChargeBytes(uint64(len(data)))
}

func commitmentNodeChargeBytes(payload uint64) uint64 {
	// A fetched node and its decoded representation coexist during decode.
	// Root readers additionally copy the decoded payload into their borrowed
	// view, so retain a third payload-sized allowance.
	if payload > (^uint64(0)-commitmentNodeOverhead)/3 {
		return ^uint64(0)
	}
	return payload*3 + commitmentNodeOverhead
}

type commitmentCursor struct {
	next       ChangeIterator
	pending    Change
	hasPending bool
	done       bool
	last       [32]byte
	hasLast    bool
}

func (c *commitmentCursor) peek(ctx context.Context, mapType Type) (*Change, error) {
	if c.hasPending {
		return &c.pending, nil
	}
	if c.done {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	change, err := c.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			c.done = true
			return nil, nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.hasLast && bytes.Compare(change.Key[:], c.last[:]) <= 0 {
		return nil, fmt.Errorf("shamap: changes are not strictly sorted and unique")
	}
	if err := validateCommitmentChange(mapType, change); err != nil {
		return nil, err
	}
	c.pending = change
	c.hasPending = true
	c.last = change.Key
	c.hasLast = true
	return &c.pending, nil
}

func (c *commitmentCursor) take() Change {
	change := c.pending
	c.pending = Change{}
	c.hasPending = false
	return change
}

func validateCommitmentChange(mapType Type, change Change) error {
	if change.Delete {
		if len(change.Data) != 0 {
			return errors.New("shamap: deletion must not carry data")
		}
		return nil
	}
	if len(change.Data) < 12 {
		return errItemTooSmall
	}
	nodeType := change.Type
	if nodeType == NodeType(0) {
		switch mapType {
		case TypeState:
			nodeType = NodeTypeAccountState
		case TypeTransaction:
			nodeType = NodeTypeTransactionNoMeta
		default:
			return fmt.Errorf("shamap: unknown map type %v", mapType)
		}
	}
	switch mapType {
	case TypeState:
		if nodeType != NodeTypeAccountState {
			return fmt.Errorf("shamap: state change has leaf type %v", nodeType)
		}
		if isZeroHash(change.Key) {
			return errors.New("shamap: state change has a zero key")
		}
	case TypeTransaction:
		if nodeType != NodeTypeTransactionNoMeta && nodeType != NodeTypeTransactionWithMeta {
			return fmt.Errorf("shamap: transaction change has leaf type %v", nodeType)
		}
		if nodeType == NodeTypeTransactionNoMeta {
			derived := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), change.Data)
			if derived != change.Key {
				return errors.New("shamap: transaction change key does not match its serialized transaction")
			}
		}
	default:
		return fmt.Errorf("shamap: unknown map type %v", mapType)
	}
	return nil
}

func validateCommitmentKeyPath(key, path [32]byte, depth int) error {
	if depth < 0 || depth > maxDepth {
		return fmt.Errorf("shamap: leaf path depth %d is outside the authenticated range", depth)
	}
	if !commitmentPathPrefixEq(key, path, depth) {
		return fmt.Errorf("shamap: leaf key %x does not match authenticated path at depth %d", [8]byte(key[:8]), depth)
	}
	return nil
}

func commitmentPathPrefixEq(a, b [32]byte, depth int) bool {
	if depth <= 0 {
		return true
	}
	fullBytes := depth / 2
	if !bytes.Equal(a[:fullBytes], b[:fullBytes]) {
		return false
	}
	if depth&1 != 0 {
		return a[fullBytes]&0xf0 == b[fullBytes]&0xf0
	}
	return true
}

func validateCommitmentLeafType(mapType Type, nodeType NodeType) error {
	switch mapType {
	case TypeState:
		if nodeType != NodeTypeAccountState {
			return fmt.Errorf("shamap: state root contains leaf type %v", nodeType)
		}
	case TypeTransaction:
		if nodeType != NodeTypeTransactionNoMeta && nodeType != NodeTypeTransactionWithMeta {
			return fmt.Errorf("shamap: transaction root contains leaf type %v", nodeType)
		}
	default:
		return fmt.Errorf("shamap: unknown map type %v", mapType)
	}
	return nil
}

func defaultCommitmentLeafType(mapType Type) (NodeType, error) {
	switch mapType {
	case TypeState:
		return NodeTypeAccountState, nil
	case TypeTransaction:
		return NodeTypeTransactionNoMeta, nil
	default:
		return NodeType(0), fmt.Errorf("shamap: unknown map type %v", mapType)
	}
}

type commitmentRef struct {
	hash [32]byte
	kind NodeType
	key  [32]byte
}

func (r commitmentRef) empty() bool {
	return r.kind == NodeType(0) || isZeroHash(r.hash)
}

type commitmentBase struct {
	commitmentRef
	branches uint16
	hashes   [BranchFactor][32]byte
	charge   uint64
}

func (b *commitmentBase) release(budget CommitmentBudget) {
	if b == nil || b.charge == 0 || budget == nil {
		return
	}
	budget.Release(b.charge)
	b.charge = 0
}

type commitmentResult struct {
	ref     commitmentRef
	changed bool
	deleted bool
}

type commitmentBuilder struct {
	ctx       context.Context
	mapType   Type
	ledgerSeq uint32
	access    *familyAccess
	budget    CommitmentBudget
	flush     func() error
	emit      ChangeSink
	cursor    commitmentCursor
	result    BuildResult

	activeFrontier uint64
}

func (b *commitmentBuilder) reserve(bytes uint64) error {
	return reserveCommitmentMemoryWithFlush(b.ctx, b.budget, bytes, b.flush)
}

// BuildChanges computes the new root from a verified parent root and a sorted
// stream of final changes. It reads only affected parent paths and emits each
// changed leaf/inner node once, post-order. Untouched subtrees remain hash-only
// references to the parent. The traversal keeps at most one active frame per
// SHAMap depth; decoded node payloads are released after branch metadata is
// copied. Output Data is borrowed by emit until that callback returns.
func BuildChanges(
	ctx context.Context,
	mapType Type,
	parentRoot [32]byte,
	ledgerSeq uint32,
	family Family,
	next ChangeIterator,
	emit ChangeSink,
	options ...CommitmentOption,
) (BuildResult, error) {
	ctx = normalizeCommitmentContext(ctx)
	if next == nil {
		return BuildResult{}, errors.New("shamap: nil change iterator")
	}
	if emit == nil {
		return BuildResult{}, errors.New("shamap: nil change sink")
	}
	settings := commitmentOptions{}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	if _, err := defaultCommitmentLeafType(mapType); err != nil {
		return BuildResult{}, err
	}
	builder := &commitmentBuilder{
		ctx:       ctx,
		mapType:   mapType,
		ledgerSeq: ledgerSeq,
		access:    bindFamily(family),
		budget:    settings.budget,
		flush:     settings.flush,
		emit:      emit,
		cursor:    commitmentCursor{next: next},
	}

	base := commitmentBase{}
	var err error
	if !isZeroHash(parentRoot) {
		base, err = builder.load(parentRoot)
		if err != nil {
			return BuildResult{}, err
		}
		if base.kind != NodeTypeInner {
			base.release(builder.budget)
			return BuildResult{}, fmt.Errorf("shamap: parent root %x is not an inner node", [8]byte(parentRoot[:8]))
		}
	}
	result, err := builder.build(base, 0, [32]byte{}, true)
	if err != nil {
		return BuildResult{}, err
	}
	if pending, err := builder.cursor.peek(ctx, mapType); err != nil {
		return BuildResult{}, err
	} else if pending != nil {
		return BuildResult{}, errors.New("shamap: change iterator returned an unconsumed change")
	}
	builder.result.Root = result.ref.hash
	return builder.result, nil
}

func (b *commitmentBuilder) enter() error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	b.activeFrontier++
	if b.activeFrontier > b.result.MaxFrontier {
		b.result.MaxFrontier = b.activeFrontier
	}
	return nil
}

func (b *commitmentBuilder) leave() {
	if b.activeFrontier > 0 {
		b.activeFrontier--
	}
}

func (b *commitmentBuilder) hasPrefix(path [32]byte, depth int) (bool, error) {
	change, err := b.cursor.peek(b.ctx, b.mapType)
	if err != nil {
		return false, err
	}
	return change != nil && commitmentPathPrefixEq(change.Key, path, depth), nil
}

func (b *commitmentBuilder) load(hash [32]byte) (commitmentBase, error) {
	if err := b.ctx.Err(); err != nil {
		return commitmentBase{}, err
	}
	if isZeroHash(hash) {
		return commitmentBase{}, nil
	}
	if b.access == nil || !b.access.available() {
		return commitmentBase{}, errors.New("shamap: incremental commitment requires a backing Family")
	}
	data, err := b.access.fetch(b.ctx, hash)
	if err != nil {
		return commitmentBase{}, fmt.Errorf("shamap: fetch parent node %x: %w", [8]byte(hash[:8]), err)
	}
	if len(data) == 0 {
		return commitmentBase{}, fmt.Errorf("shamap: fetch parent node %x: %w", [8]byte(hash[:8]), &MissingNodeError{Hash: hash})
	}
	charge := commitmentNodeCharge(data)
	if err := b.reserve(charge); err != nil {
		return commitmentBase{}, err
	}
	node, err := decodeCommitmentNode(data, hash)
	if err != nil {
		releaseCommitmentMemory(b.budget, charge)
		return commitmentBase{}, fmt.Errorf("shamap: decode parent node %x: %w", [8]byte(hash[:8]), err)
	}
	base := commitmentBase{commitmentRef: commitmentRef{hash: hash}, charge: charge}
	if node.inner {
		base.kind = NodeTypeInner
		base.branches = node.branches
		base.hashes = node.hashes
	} else {
		base.kind = node.leafType
		if err := validateCommitmentLeafType(b.mapType, base.kind); err != nil {
			base.release(b.budget)
			return commitmentBase{}, err
		}
		base.key = node.key
	}
	return base, nil
}

func (b *commitmentBuilder) build(base commitmentBase, depth int, path [32]byte, root bool) (result commitmentResult, err error) {
	defer base.release(b.budget)
	if err := b.enter(); err != nil {
		return commitmentResult{}, err
	}
	defer b.leave()
	if depth > maxDepth {
		return commitmentResult{}, errors.New("shamap: commitment exceeded maximum depth")
	}
	if base.kind == NodeTypeInner {
		if depth >= maxDepth {
			return commitmentResult{}, fmt.Errorf("shamap: inner node at depth %d exceeds maximum depth", depth)
		}
	} else if base.kind != NodeType(0) {
		if err := validateCommitmentKeyPath(base.key, path, depth); err != nil {
			return commitmentResult{}, err
		}
	}
	base.release(b.budget)
	if depth == maxDepth {
		return b.buildTerminal(base)
	}

	hasChanges, err := b.hasPrefix(path, depth)
	if err != nil {
		return commitmentResult{}, err
	}
	if !hasChanges {
		return commitmentResult{ref: base.commitmentRef}, nil
	}
	switch base.kind {
	case NodeType(0):
		return b.buildEmpty(depth, path, root)
	case NodeTypeInner:
		return b.buildInner(base, depth, path, root)
	case NodeTypeTransactionNoMeta, NodeTypeTransactionWithMeta, NodeTypeAccountState:
		return b.buildLeaf(base, depth, path, root)
	default:
		return commitmentResult{}, fmt.Errorf("shamap: invalid parent node type %v", base.kind)
	}
}

func (b *commitmentBuilder) buildTerminal(base commitmentBase) (commitmentResult, error) {
	change, err := b.cursor.peek(b.ctx, b.mapType)
	if err != nil {
		return commitmentResult{}, err
	}
	if change == nil {
		return commitmentResult{ref: base.commitmentRef}, nil
	}
	if base.kind != NodeType(0) && change.Key != base.key {
		return commitmentResult{}, errors.New("shamap: distinct keys reached the same terminal path")
	}
	changeValue := b.cursor.take()
	if changeValue.Delete {
		if base.kind == NodeType(0) {
			return commitmentResult{}, nil
		}
		return commitmentResult{changed: true, deleted: true}, nil
	}
	ref, err := b.emitLeaf(changeValue)
	if err != nil {
		return commitmentResult{}, err
	}
	if base.kind == NodeType(0) {
		return commitmentResult{ref: ref, changed: true}, nil
	}
	return commitmentResult{ref: ref, changed: ref.hash != base.hash || ref.kind != base.kind}, nil
}

func (b *commitmentBuilder) buildEmpty(depth int, path [32]byte, root bool) (commitmentResult, error) {
	return b.buildEmptyGroup(depth, path, root, commitmentRef{})
}

func (b *commitmentBuilder) buildEmptyGroup(depth int, path [32]byte, root bool, seed commitmentRef) (commitmentResult, error) {
	if depth > maxDepth {
		return commitmentResult{}, errors.New("shamap: commitment exceeded maximum depth")
	}
	if seed.empty() {
		var err error
		seed, err = b.takeEmptyLeaf(path, depth)
		if err != nil {
			return commitmentResult{}, err
		}
		if seed.empty() {
			return commitmentResult{}, nil
		}
	}
	if depth == maxDepth {
		return commitmentResult{ref: seed, changed: true}, nil
	}

	change, err := b.nextEmptyChange(path, depth)
	if err != nil {
		return commitmentResult{}, err
	}
	if change == nil {
		return b.finishEmptyLeaf(depth, root, seed)
	}

	var (
		hashes   [BranchFactor][32]byte
		branches uint16
		kinds    [BranchFactor]NodeType
		keys     [BranchFactor][32]byte
	)
	branch := getBranchAtDepth(seed.key, depth)
	childPath := childPathForBranch(path, depth, branch)
	child, err := b.buildEmptyChild(depth+1, childPath, seed)
	if err != nil {
		return commitmentResult{}, err
	}
	if !child.ref.empty() {
		hashes[branch] = child.ref.hash
		kinds[branch] = child.ref.kind
		keys[branch] = child.ref.key
		branches |= 1 << branch
	}
	for {
		change, err := b.nextEmptyChange(path, depth)
		if err != nil {
			return commitmentResult{}, err
		}
		if change == nil {
			break
		}
		branch = getBranchAtDepth(change.Key, depth)
		childPath = childPathForBranch(path, depth, branch)
		child, err = b.buildEmptyChild(depth+1, childPath, commitmentRef{})
		if err != nil {
			return commitmentResult{}, err
		}
		if child.ref.empty() {
			continue
		}
		hashes[branch] = child.ref.hash
		kinds[branch] = child.ref.kind
		keys[branch] = child.ref.key
		branches |= 1 << branch
	}
	if !root && bits.OnesCount16(branches) == 1 {
		branch := bits.TrailingZeros16(branches)
		if kinds[branch] != NodeTypeInner {
			return commitmentResult{
				ref:     commitmentRef{hash: hashes[branch], kind: kinds[branch], key: keys[branch]},
				changed: true,
			}, nil
		}
	}
	ref, err := b.emitInner(hashes, branches)
	if err != nil {
		return commitmentResult{}, err
	}
	return commitmentResult{ref: ref, changed: true}, nil
}

func (b *commitmentBuilder) nextEmptyChange(path [32]byte, depth int) (*Change, error) {
	for {
		change, err := b.cursor.peek(b.ctx, b.mapType)
		if err != nil {
			return nil, err
		}
		if change == nil || !commitmentPathPrefixEq(change.Key, path, depth) {
			return nil, nil
		}
		if !change.Delete {
			return change, nil
		}
		b.cursor.take()
	}
}

func (b *commitmentBuilder) takeEmptyLeaf(path [32]byte, depth int) (commitmentRef, error) {
	change, err := b.nextEmptyChange(path, depth)
	if err != nil || change == nil {
		return commitmentRef{}, err
	}
	value := b.cursor.take()
	return b.emitLeaf(value)
}

func (b *commitmentBuilder) finishEmptyLeaf(depth int, root bool, seed commitmentRef) (commitmentResult, error) {
	if !root {
		return commitmentResult{ref: seed, changed: true}, nil
	}
	branch := getBranchAtDepth(seed.key, depth)
	var hashes [BranchFactor][32]byte
	hashes[branch] = seed.hash
	ref, err := b.emitInner(hashes, 1<<branch)
	if err != nil {
		return commitmentResult{}, err
	}
	return commitmentResult{ref: ref, changed: true}, nil
}

func (b *commitmentBuilder) buildEmptyChild(depth int, path [32]byte, seed commitmentRef) (result commitmentResult, err error) {
	if err := b.enter(); err != nil {
		return commitmentResult{}, err
	}
	defer b.leave()
	return b.buildEmptyGroup(depth, path, false, seed)
}

func (b *commitmentBuilder) buildInner(base commitmentBase, depth int, path [32]byte, root bool) (commitmentResult, error) {
	hashes := base.hashes
	kinds := [BranchFactor]NodeType{}
	keys := [BranchFactor][32]byte{}
	var changed, deleted bool
	for branch := 0; branch < BranchFactor; branch++ {
		childPath := childPathForBranch(path, depth, branch)
		hasChanges, err := b.hasPrefix(childPath, depth+1)
		if err != nil {
			return commitmentResult{}, err
		}
		if !hasChanges {
			continue
		}
		var childBase commitmentBase
		if base.branches&(1<<branch) != 0 {
			childBase, err = b.load(base.hashes[branch])
			if err != nil {
				return commitmentResult{}, err
			}
		}
		child, err := b.build(childBase, depth+1, childPath, false)
		if err != nil {
			return commitmentResult{}, err
		}
		changed = changed || child.changed
		deleted = deleted || child.deleted
		if child.ref.empty() {
			hashes[branch] = [32]byte{}
			base.branches &= ^(1 << branch)
			continue
		}
		hashes[branch] = child.ref.hash
		kinds[branch] = child.ref.kind
		keys[branch] = child.ref.key
		base.branches |= 1 << branch
	}
	if !changed {
		return commitmentResult{ref: base.commitmentRef}, nil
	}
	branches := base.branches
	if branches == 0 {
		if root {
			return commitmentResult{changed: true, deleted: deleted}, nil
		}
		return commitmentResult{changed: true, deleted: deleted}, nil
	}
	if !root && deleted && bits.OnesCount16(branches) == 1 {
		branch := bits.TrailingZeros16(branches)
		kind := kinds[branch]
		if kind == NodeType(0) {
			childBase, err := b.load(hashes[branch])
			if err != nil {
				return commitmentResult{}, err
			}
			kind = childBase.kind
			keys[branch] = childBase.key
			childBase.release(b.budget)
		}
		if kind != NodeTypeInner {
			return commitmentResult{
				ref:     commitmentRef{hash: hashes[branch], kind: kind, key: keys[branch]},
				changed: true,
				deleted: true,
			}, nil
		}
	}
	ref, err := b.emitInner(hashes, branches)
	if err != nil {
		return commitmentResult{}, err
	}
	return commitmentResult{ref: ref, changed: true, deleted: deleted}, nil
}

func (b *commitmentBuilder) buildLeaf(base commitmentBase, depth int, path [32]byte, root bool) (commitmentResult, error) {
	result := commitmentResult{
		ref: commitmentRef{hash: base.hash, kind: base.kind, key: base.key},
	}
	change, err := b.cursor.peek(b.ctx, b.mapType)
	if err != nil {
		return commitmentResult{}, err
	}
	if change == nil || !commitmentPathPrefixEq(change.Key, path, depth) {
		return result, nil
	}
	if change.Key == base.key {
		changeValue := b.cursor.take()
		if changeValue.Delete {
			result = commitmentResult{changed: true, deleted: true}
		} else {
			ref, err := b.emitLeaf(changeValue)
			if err != nil {
				return commitmentResult{}, err
			}
			result.changed = ref.hash != base.hash || ref.kind != base.kind
			result.ref = ref
		}
	}
	hasChanges, err := b.hasPrefix(path, depth)
	if err != nil {
		return commitmentResult{}, err
	}
	if !hasChanges {
		return result, nil
	}
	if result.ref.empty() {
		empty, err := b.buildEmpty(depth, path, root)
		if err != nil {
			return commitmentResult{}, err
		}
		empty.changed = true
		empty.deleted = empty.deleted || result.deleted
		return empty, nil
	}
	partition, err := b.buildLeafPartition(result.ref, depth, path, root)
	if err != nil {
		return commitmentResult{}, err
	}
	partition.changed = partition.changed || result.changed
	partition.deleted = partition.deleted || result.deleted
	return partition, nil
}

func (b *commitmentBuilder) buildLeafPartition(base commitmentRef, depth int, path [32]byte, root bool) (commitmentResult, error) {
	var (
		hashes   [BranchFactor][32]byte
		kinds    [BranchFactor]NodeType
		keys     [BranchFactor][32]byte
		branches uint16
		changed  bool
		deleted  bool
	)
	baseBranch := getBranchAtDepth(base.key, depth)
	for branch := 0; branch < BranchFactor; branch++ {
		childPath := childPathForBranch(path, depth, branch)
		hasChanges, err := b.hasPrefix(childPath, depth+1)
		if err != nil {
			return commitmentResult{}, err
		}
		if !hasChanges && branch != baseBranch {
			continue
		}
		childBase := commitmentBase{}
		if branch == baseBranch {
			childBase = commitmentBase{commitmentRef: base}
		}
		if !hasChanges {
			hashes[branch] = base.hash
			kinds[branch] = base.kind
			keys[branch] = base.key
			branches |= 1 << branch
			continue
		}
		child, err := b.build(childBase, depth+1, childPath, false)
		if err != nil {
			return commitmentResult{}, err
		}
		changed = changed || child.changed
		deleted = deleted || child.deleted
		if child.ref.empty() {
			continue
		}
		hashes[branch] = child.ref.hash
		kinds[branch] = child.ref.kind
		keys[branch] = child.ref.key
		branches |= 1 << branch
	}
	if branches == 0 {
		return commitmentResult{changed: changed, deleted: deleted}, nil
	}
	if !root && bits.OnesCount16(branches) == 1 {
		branch := bits.TrailingZeros16(branches)
		if kinds[branch] != NodeTypeInner {
			return commitmentResult{
				ref:     commitmentRef{hash: hashes[branch], kind: kinds[branch], key: keys[branch]},
				changed: changed,
				deleted: deleted,
			}, nil
		}
	}
	ref, err := b.emitInner(hashes, branches)
	if err != nil {
		return commitmentResult{}, err
	}
	return commitmentResult{ref: ref, changed: true, deleted: deleted}, nil
}

func (b *commitmentBuilder) emitLeaf(change Change) (commitmentRef, error) {
	nodeType := change.Type
	if nodeType == NodeType(0) {
		var err error
		nodeType, err = defaultCommitmentLeafType(b.mapType)
		if err != nil {
			return commitmentRef{}, err
		}
	}
	charge := commitmentNodeCharge(change.Data)
	if err := b.reserve(charge); err != nil {
		return commitmentRef{}, err
	}
	node, err := createLeafNode(nodeType, NewItem(change.Key, change.Data))
	if err != nil {
		releaseCommitmentMemory(b.budget, charge)
		return commitmentRef{}, err
	}
	entry, err := flushEntryForNode(node, b.ledgerSeq, b.mapType)
	if err != nil {
		if b.budget != nil {
			b.budget.Release(charge)
		}
		return commitmentRef{}, err
	}
	if err := b.emitEntry(entry, false, charge); err != nil {
		return commitmentRef{}, err
	}
	return commitmentRef{hash: entry.Hash, kind: nodeType, key: change.Key}, nil
}

func (b *commitmentBuilder) emitInner(hashes [BranchFactor][32]byte, branches uint16) (commitmentRef, error) {
	if branches == 0 {
		return commitmentRef{}, nil
	}
	charge := commitmentNodeChargeBytes(uint64(fullInnerSerializedSize))
	if err := b.reserve(charge); err != nil {
		return commitmentRef{}, err
	}
	node := &innerNode{hashes: hashes, isBranch: branches}
	if err := node.UpdateHash(); err != nil {
		releaseCommitmentMemory(b.budget, charge)
		return commitmentRef{}, err
	}
	data, err := node.SerializeWithPrefix()
	if err != nil {
		releaseCommitmentMemory(b.budget, charge)
		return commitmentRef{}, err
	}
	entry := FlushEntry{Hash: node.Hash(), Data: data, LedgerSeq: b.ledgerSeq, MapType: b.mapType}
	if err := b.emitEntry(entry, true, charge); err != nil {
		return commitmentRef{}, err
	}
	return commitmentRef{hash: entry.Hash, kind: NodeTypeInner}, nil
}

func (b *commitmentBuilder) emitEntry(entry FlushEntry, inner bool, charge uint64) error {
	b.result.Nodes++
	if !inner {
		b.result.Leaves++
	}
	b.result.Bytes += uint64(len(entry.Data))
	err := b.emit(entry)
	if b.budget != nil {
		b.budget.Release(charge)
	}
	if err != nil {
		return fmt.Errorf("shamap: emit node %x: %w", [8]byte(entry.Hash[:8]), err)
	}
	return b.ctx.Err()
}

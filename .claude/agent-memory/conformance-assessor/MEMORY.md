# Conformance Assessor Memory

## SHAMap Assessment (2026-03-10, updated)
- [shamap-assessment.md](shamap-assessment.md) - Detailed SHAMap conformance findings

### Status: Mostly Conformant (no critical bugs)
- 144 test assertions pass, 0 fail, 0 skip
- Hash computation (SHA-512 Half with prefixes) matches rippled exactly for all 4 node types
- Wire serialization matches for all 5 wire types (TransactionLeafNode key bug was FIXED)
- Wire type constants match: tx=0, accountState=1, inner=2, compressedInner=3, txWithMeta=4
- NodeID branching/selectBranch logic matches rippled exactly
- MaxDepth=64 matches rippled's leafDepth=64
- Store (prefix) serialization/deserialization matches rippled makeFromPrefix()

### Minor Deviations (non-critical)
- Leaf minimum size: rippled asserts item size >= 12 bytes, Go has no check
- Compare for backed maps: Go descends children, rippled compares stored hashes (perf only)
- InnerNode.Clone(): Go deep-clones recursively, rippled shallow-shares (perf only)
- Unbacked snapshot: Go O(tree) deep clone, rippled O(1) COW via cowid (perf only)
- Delete replacement leaf type: Go derives from map type, rippled from original leaf (same in practice)

### Previously Fixed
- TransactionLeafNode.SerializeForWire() key inclusion: FIXED (was critical, now correct)

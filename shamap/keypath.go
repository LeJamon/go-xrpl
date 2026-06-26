package shamap

const (
	// MaxDepth is the maximum depth of the SHAMap tree (256 bits / 4 bits per branch).
	MaxDepth = 64
	// BranchMask is the mask for valid branch values (0-15).
	BranchMask = 0x0F
)

// selectBranch returns which branch of a node (at depth nodeID.depth) contains
// the given key.
func selectBranch(nodeID NodeID, key [32]byte) uint8 {
	depth := nodeID.depth
	if depth >= MaxDepth {
		return 0
	}
	byteIndex := depth / 2
	if byteIndex >= 32 {
		return 0
	}
	b := key[byteIndex] //nolint:gosec // G602: byteIndex < 32 guarded above
	if depth%2 == 0 {
		return b >> 4
	}
	return b & BranchMask
}

// getBranchAtDepth extracts the 4-bit branch value at position depth in key.
func getBranchAtDepth(key [32]byte, depth int) int {
	if depth >= MaxDepth {
		return 0
	}
	byteIndex := depth / 2
	if byteIndex >= 32 {
		return 0
	}
	b := key[byteIndex] //nolint:gosec // G602: byteIndex < 32 guarded above
	if depth%2 == 0 {
		return int(b >> 4)
	}
	return int(b & 0x0F)
}

// findSplitDepth finds the first depth at which the two keys differ, starting
// from startDepth.
func findSplitDepth(key1, key2 [32]byte, startDepth int) int {
	for depth := startDepth; depth < MaxDepth; depth++ {
		if getBranchAtDepth(key1, depth) != getBranchAtDepth(key2, depth) {
			return depth
		}
	}
	return MaxDepth - 1
}

// childPathForBranch returns the child's path after following branch at depth.
func childPathForBranch(parentPath [32]byte, depth, branch int) [32]byte {
	out := parentPath
	bytePos := depth / 2
	if depth%2 == 0 {
		out[bytePos] = (out[bytePos] & 0x0F) | (byte(branch) << 4)
	} else {
		out[bytePos] = (out[bytePos] & 0xF0) | byte(branch)
	}
	return out
}

// selectBranchForPath returns the branch nibble at position depth of path.
func selectBranchForPath(path [32]byte, depth int) int {
	bytePos := depth / 2
	if depth%2 == 0 {
		return int(path[bytePos] >> 4)
	}
	return int(path[bytePos] & 0x0F)
}

// pathPrefixEq compares the first depth nibbles of a and b.
func pathPrefixEq(a, b [32]byte, depth int) bool {
	for d := 0; d < depth; d++ {
		if selectBranchForPath(a, d) != selectBranchForPath(b, d) {
			return false
		}
	}
	return true
}

// isZeroHash returns true if every byte of hash is zero.
func isZeroHash(hash [32]byte) bool {
	return hash == [32]byte{}
}

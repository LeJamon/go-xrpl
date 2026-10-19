package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// FetchBounded reads a prefix node without copying an oversized record or
// promoting peer reads into the foreground cache.
func (f *NodeStore) FetchBounded(ctx context.Context, hash [32]byte, maxBytes int) ([]byte, bool, error) {
	db, ok := f.db.(interface {
		FetchPeerBounded(context.Context, nodestore.Hash256, int) (*nodestore.Node, error)
	})
	if !ok {
		return nil, false, fmt.Errorf("shamap: bounded NodeStore read: %w", errors.ErrUnsupported)
	}
	node, err := db.FetchPeerBounded(ctx, nodestore.Hash256(hash), maxBytes)
	if errors.Is(err, kvstore.ErrValueTooLarge) {
		return nil, true, nil
	}
	if err != nil || node == nil {
		return nil, false, err
	}
	return node.Data, false, nil
}

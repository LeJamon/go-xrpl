package ledger

import (
	"context"
	"errors"
	"testing"
)

func TestNewOpenFromParentHeaderHonorsCancellation(t *testing.T) {
	parent := newParentAt(t, 2, 30, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewOpenFromParentHeader(ctx, parent, parent.Header()); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewOpenFromParentHeader error = %v, want context.Canceled", err)
	}
}

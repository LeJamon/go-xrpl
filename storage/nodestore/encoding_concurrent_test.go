package nodestore_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/kvstore/memorydb"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
)

// TestConcurrentStoreEncodeBuf guards against the encodeBufPool aliasing bug:
// acquireEncodeBuf must hand the pooled buffer to exactly one caller, not
// return it to the pool while a caller still holds it. The pool is package
// global, so two independent databases storing concurrently both draw from
// it. With the bug, one store's encode write races the other store's backend
// copy and silently corrupts the stored bytes. Run with -race to catch the
// data race; the read-back assertions catch the corruption on their own.
func TestConcurrentStoreEncodeBuf(t *testing.T) {
	const (
		dbs            = 2
		goroutinesPer  = 8
		batchesPerGoro = 12
		nodesPerBatch  = 16
		// Payloads stay under the pool's 1024-byte buffer so every encode
		// hits the reuse branch — the one that aliased.
		payloadSize = 200
	)

	makeNode := func(seed int) *nodestore.Node {
		data := make(nodestore.Blob, payloadSize)
		for i := range data {
			data[i] = byte(seed + i)
		}
		return nodestore.NewNode(nodestore.NodeTransaction, data)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, dbs*goroutinesPer)

	for d := 0; d < dbs; d++ {
		db := nodestore.NewKVDatabase(memorydb.New(), fmt.Sprintf("db%d", d), 0, time.Minute)
		for g := 0; g < goroutinesPer; g++ {
			wg.Add(1)
			go func(d, g int) {
				defer wg.Done()
				for b := 0; b < batchesPerGoro; b++ {
					nodes := make([]*nodestore.Node, nodesPerBatch)
					for n := range nodes {
						// Unique seed per node so each has a distinct hash/key.
						nodes[n] = makeNode((((d*goroutinesPer+g)*batchesPerGoro+b)*nodesPerBatch + n) * payloadSize)
					}
					if err := db.StoreBatch(ctx, nodes); err != nil {
						errCh <- fmt.Errorf("db%d goroutine%d: store: %w", d, g, err)
						return
					}
					for _, want := range nodes {
						got, err := db.Fetch(ctx, want.Hash)
						if err != nil {
							errCh <- fmt.Errorf("db%d goroutine%d: fetch: %w", d, g, err)
							return
						}
						if !bytes.Equal(got.Data, want.Data) {
							errCh <- fmt.Errorf("db%d goroutine%d: payload corrupted on read-back", d, g)
							return
						}
					}
				}
			}(d, g)
		}
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// DownloadShardMethod handles the download_shard RPC method
type DownloadShardMethod struct{ AdminHandler }

func (m *DownloadShardMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	return map[string]any{"message": "shard download initiated"}, nil
}

// CrawlShardsMethod handles the crawl_shards RPC method
type CrawlShardsMethod struct{ AdminHandler }

func (m *CrawlShardsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	return map[string]any{"shards": []any{}}, nil
}

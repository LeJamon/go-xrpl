package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// DownloadShardMethod handles the download_shard RPC method
type DownloadShardMethod struct{}

func (m *DownloadShardMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{"message": "shard download initiated"}, nil
}

func (m *DownloadShardMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *DownloadShardMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

// CrawlShardsMethod handles the crawl_shards RPC method
type CrawlShardsMethod struct{}

func (m *CrawlShardsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{"shards": []interface{}{}}, nil
}

func (m *CrawlShardsMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *CrawlShardsMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

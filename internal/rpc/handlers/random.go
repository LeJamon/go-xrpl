package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// RandomMethod handles the random RPC method
type RandomMethod struct{ BaseHandler }

func (m *RandomMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Generate 256 bits (32 bytes) of cryptographically secure random data
	randomBytes := make([]byte, 32)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to generate random data: " + err.Error())
	}

	response := map[string]interface{}{
		"random": strings.ToUpper(hex.EncodeToString(randomBytes)),
	}

	return response, nil
}

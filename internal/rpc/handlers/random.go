package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type randomMethod struct{ baseHandler }

func (m *randomMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	// Generate 256 bits (32 bytes) of cryptographically secure random data
	randomBytes := make([]byte, 32)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return nil, rpcInternalError("random: random data generation failed", err)
	}

	response := map[string]any{
		"random": strings.ToUpper(hex.EncodeToString(randomBytes)),
	}

	return response, nil
}

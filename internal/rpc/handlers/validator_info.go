package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ValidatorInfoMethod handles the `validator_info` RPC method.
//
// Rippled reference: src/xrpld/rpc/handlers/ValidatorInfo.cpp:30-62.
// The notValidator gate maps to rippled's not_validator_error() —
// make_param_error("not a validator") — i.e. rpcINVALID_PARAMS with
// the literal "not a validator" message. SDK tooling (xrpl.js, xrpl-py)
// pattern-matches on that exact wire shape.
type ValidatorInfoMethod struct{ AdminHandler }

type validatorInfoResponse struct {
	MasterKey    string `json:"master_key,omitempty"`
	EphemeralKey string `json:"ephemeral_key,omitempty"`
	Manifest     string `json:"manifest,omitempty"`
	// Pointer so a legitimate seq=0 still serialises (rippled emits
	// `ret[jss::seq] = *seq` regardless of value); nil is dropped by
	// omitempty when the manifest cache had no sequence to report.
	Seq    *uint32 `json:"seq,omitempty"`
	Domain string  `json:"domain,omitempty"`
}

func (m *ValidatorInfoMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil || len(ctx.Services.ValidatorPublicKey) == 0 {
		return nil, types.RpcErrorInvalidParams("not a validator")
	}

	validationPK := ctx.Services.ValidatorPublicKey
	if len(validationPK) != 33 {
		return nil, types.RpcErrorInternal("validator public key has invalid length")
	}

	var keyArr [33]byte
	copy(keyArr[:], validationPK)

	masterKey := keyArr
	if ctx.Services.Manifests != nil {
		masterKey = ctx.Services.Manifests.GetMasterKey(keyArr)
	}

	masterB58, err := addresscodec.EncodeNodePublicKey(masterKey[:])
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("encode master key: %v", err))
	}
	resp := validatorInfoResponse{MasterKey: masterB58}

	// rippled: `if (mk == validationPK) return ret;` — only emit the
	// ephemeral / manifest / seq / domain block when a manifest cache
	// resolved the configured signing key to a different master.
	// (masterKey != keyArr already implies Manifests != nil, since the
	// nil branch above leaves masterKey == keyArr.)
	if masterKey != keyArr {
		ephB58, err := addresscodec.EncodeNodePublicKey(keyArr[:])
		if err != nil {
			return nil, types.RpcErrorInternal(fmt.Sprintf("encode ephemeral key: %v", err))
		}
		resp.EphemeralKey = ephB58

		if manifestBytes, ok := ctx.Services.Manifests.GetManifest(masterKey); ok {
			resp.Manifest = base64.StdEncoding.EncodeToString(manifestBytes)
		}
		if seq, ok := ctx.Services.Manifests.GetSequence(masterKey); ok {
			s := seq
			resp.Seq = &s
		}
		if domain, ok := ctx.Services.Manifests.GetDomain(masterKey); ok {
			resp.Domain = domain
		}
	}

	return resp, nil
}

package adaptor

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

func (r *Router) handleProofPathResponse(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeProofPathResponse, msg.Payload)
	if err != nil {
		return
	}
	resp := decoded.(*message.ProofPathResponse)
	if resp.HasError() {
		msg.SelectPeerCharge(resource.FeeInvalidData(), "proof_path_response")
	} else if !validSkipListProof(resp) {
		msg.SelectPeerCharge(resource.FeeMalformedData(), "proof_path_response malformed")
	}
}

func validSkipListProof(resp *message.ProofPathResponse) bool {
	key := keylet.LedgerHashes().Key
	if resp.MapType != message.LedgerMapAccountState || !bytes.Equal(resp.Key, key[:]) || len(resp.LedgerHash) != 32 {
		return false
	}
	h, err := decodePeerLedgerHeader(resp.LedgerHeader)
	if err != nil || h.Hash != [32]byte(resp.LedgerHash) {
		return false
	}
	return shamap.VerifyProofPath(h.AccountHash, key, resp.Path)
}

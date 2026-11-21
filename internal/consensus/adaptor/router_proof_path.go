package adaptor

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

func proofPathResponseFee(payload []byte) resource.Charge {
	decoded, err := message.Decode(message.TypeProofPathResponse, payload)
	if err != nil {
		return resource.FeeMalformedData()
	}
	resp := decoded.(*message.ProofPathResponse)
	if resp.HasError() {
		return resource.FeeInvalidData()
	}
	key := keylet.LedgerHashes().Key
	if resp.MapType != message.LedgerMapAccountState || !bytes.Equal(resp.Key, key[:]) || len(resp.LedgerHash) != 32 {
		return resource.FeeMalformedData()
	}
	h, err := decodePeerLedgerHeader(resp.LedgerHeader)
	if err != nil || h.Hash != [32]byte(resp.LedgerHash) {
		return resource.FeeMalformedData()
	}
	if !shamap.VerifyProofPath(h.AccountHash, key, resp.Path) {
		return resource.FeeMalformedData()
	}
	// Replay uses locally resolved ancestors, so there is no pending skip-list
	// request to complete. An unsolicited, verified proof carries the usual fee.
	return resource.FeeTrivialPeer()
}

package tx

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Regression for the mixed-network empty-tx-tree fork: binary metadata
// serialization keyed DeliveredAmount as the RPC/JSON name "delivered_amount"
// (and read .Value as a method value, not a call), so binarycodec.Encode
// failed with ErrUnknownField for any tx carrying a DeliveredAmount. That
// error was swallowed in openledger.applyAndClassify (the tx was dropped from
// the consensus build while its state mutation persisted), producing a ledger
// with advanced state but a short/empty transaction tree → transaction_hash
// divergence from rippled → consensus wedge.
func TestSerializeMetadata_DeliveredAmount(t *testing.T) {
	t.Run("XRP", func(t *testing.T) {
		amt := state.NewXRPAmountFromInt(1_000_000)
		meta := &Metadata{
			TransactionResult: ter.TesSUCCESS,
			TransactionIndex:  0,
			AffectedNodes:     []AffectedNode{},
			DeliveredAmount:   &amt,
		}
		blob, err := SerializeMetadata(meta)
		if err != nil {
			t.Fatalf("SerializeMetadata with XRP DeliveredAmount must not error, got: %v", err)
		}
		if len(blob) == 0 {
			t.Fatal("expected non-empty metadata blob")
		}
		decoded, err := binarycodec.Decode(strings.ToUpper(hex.EncodeToString(blob)))
		if err != nil {
			t.Fatalf("decode round-trip: %v", err)
		}
		if _, ok := decoded["DeliveredAmount"]; !ok {
			t.Fatalf("DeliveredAmount missing from decoded binary metadata: %v", decoded)
		}
	})

	t.Run("IOU", func(t *testing.T) {
		amt := state.NewIssuedAmountFromValue(10, 0, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
		meta := &Metadata{
			TransactionResult: ter.TesSUCCESS,
			TransactionIndex:  0,
			AffectedNodes:     []AffectedNode{},
			DeliveredAmount:   &amt,
		}
		if _, err := SerializeMetadata(meta); err != nil {
			t.Fatalf("SerializeMetadata with IOU DeliveredAmount must not error, got: %v", err)
		}
	})

	// An MPT DeliveredAmount has an empty Currency, so before fixMPTDeliveredAmount
	// wiring it fell into the "no Currency ⇒ XRP" branch and serialized as an
	// 8-byte native drops amount instead of the 33-byte MPToken form — a silent
	// binary/consensus metadata fork. It must round-trip as an MPT amount.
	t.Run("MPT", func(t *testing.T) {
		mptID := "0000000AB5F762798A53D543A014CAF8B297CFF8F2F937E8"
		amt := state.NewMPTAmountWithIssuanceID(800, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", mptID)
		meta := &Metadata{
			TransactionResult: ter.TesSUCCESS,
			TransactionIndex:  0,
			AffectedNodes:     []AffectedNode{},
			DeliveredAmount:   &amt,
		}
		blob, err := SerializeMetadata(meta)
		if err != nil {
			t.Fatalf("SerializeMetadata with MPT DeliveredAmount must not error, got: %v", err)
		}
		decoded, err := binarycodec.Decode(strings.ToUpper(hex.EncodeToString(blob)))
		if err != nil {
			t.Fatalf("decode round-trip: %v", err)
		}
		da, ok := decoded["DeliveredAmount"].(map[string]any)
		if !ok {
			t.Fatalf("DeliveredAmount must decode as an MPT amount map, got: %#v", decoded["DeliveredAmount"])
		}
		if da["value"] != "800" {
			t.Fatalf("DeliveredAmount value = %v, want 800", da["value"])
		}
		if !strings.EqualFold(da["mpt_issuance_id"].(string), mptID) {
			t.Fatalf("DeliveredAmount mpt_issuance_id = %v, want %s", da["mpt_issuance_id"], mptID)
		}
	})
}

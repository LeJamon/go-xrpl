package escrow

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

var (
	escOwnerID = [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
	escDestID  = [20]byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xb4}
)

// escrowStateBytes maps an EscrowCreate onto the state.SerializeEscrow inputs —
// the exact mapping the creation path uses.
func escrowStateBytes(e *EscrowCreate, ownerID, destID [20]byte, transferRate, ownerNode, destNode uint64,
	hasDestNode bool, issuerNode uint64, hasIssuerNode, includeSequence bool) ([]byte, error) {
	var condition string
	if e.Condition != nil {
		condition = *e.Condition
	}
	var seqPtr *uint32
	if includeSequence {
		sq := e.GetCommon().SeqProxy()
		seqPtr = &sq
	}
	return state.SerializeEscrow(ownerID, destID, e.Amount, uint32(transferRate),
		ownerNode, destNode, hasDestNode, issuerNode, hasIssuerNode,
		e.FinishAfter, e.CancelAfter, condition,
		e.GetCommon().SourceTag, e.DestinationTag, seqPtr)
}

func TestEscrowSerializeGolden(t *testing.T) {
	iou := tx.NewIssuedAmountFromFloat64(100.5, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	mpt := state.NewMPTAmountWithIssuanceID(123456, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"00000539C35BFC42B69F7A19B7C4C5B5D5E7F9A1B3C5D7E9")

	cond := "A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"

	xrpMin := &EscrowCreate{Amount: tx.NewXRPAmount(1_000_000), FinishAfter: ptrUint32(700000000)}

	xrpFull := &EscrowCreate{
		Amount:         tx.NewXRPAmount(2_500_000),
		DestinationTag: ptrUint32(99),
		CancelAfter:    ptrUint32(700000100),
		FinishAfter:    ptrUint32(700000000),
		Condition:      strp(cond),
	}
	xrpFull.SourceTag = ptrUint32(7)

	iouEsc := &EscrowCreate{Amount: iou, FinishAfter: ptrUint32(700000000)}
	mptEsc := &EscrowCreate{Amount: mpt, CancelAfter: ptrUint32(700000200)}

	cases := []struct {
		name       string
		tx         *EscrowCreate
		rate       uint64
		ownerNd    uint64
		destNd     uint64
		hasDest    bool
		issuerNd   uint64
		hasIssuer  bool
		includeSeq bool
		wantHex    string
	}{
		{name: "xrp_minimal", tx: xrpMin, ownerNd: 3, hasDest: false,
			wantHex: "11007522000000002500000000202529B927003400000000000000035500000000000000000000000000000000000000000000000000000000000000006140000000000F424081140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "xrp_full_seq", tx: xrpFull, ownerNd: 1, destNd: 2, hasDest: true, includeSeq: true,
			wantHex: "11007522000000002300000007240000000025000000002E00000063202429B92764202529B927003400000000000000013900000000000000025500000000000000000000000000000000000000000000000000000000000000006140000000002625A0701127A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B85581010081140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "iou_with_transferrate", tx: iouEsc, rate: 1_005_000_000, ownerNd: 4, destNd: 5, hasDest: true, issuerNd: 6, hasIssuer: true,
			wantHex: "110075220000000025000000002B3BE71540202529B92700340000000000000004390000000000000005301B000000000000000655000000000000000000000000000000000000000000000000000000000000000061D503920ACBFFD0000000000000000000000000005553440000000000B5F762798A53D543A014CAF8B297CFF8F2F937E881140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "iou_parity_rate_omitted", tx: iouEsc, rate: 1_000_000_000, ownerNd: 4, destNd: 5, hasDest: true,
			wantHex: "11007522000000002500000000202529B9270034000000000000000439000000000000000555000000000000000000000000000000000000000000000000000000000000000061D503920ACBFFD0000000000000000000000000005553440000000000B5F762798A53D543A014CAF8B297CFF8F2F937E881140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "mpt_escrow", tx: mptEsc, ownerNd: 8, destNd: 9, hasDest: true,
			wantHex: "11007522000000002500000000202429B927C83400000000000000083900000000000000095500000000000000000000000000000000000000000000000000000000000000006160000000000001E24000000539C35BFC42B69F7A19B7C4C5B5D5E7F9A1B3C5D7E981140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := escrowStateBytes(tc.tx, escOwnerID, escDestID, tc.rate,
				tc.ownerNd, tc.destNd, tc.hasDest, tc.issuerNd, tc.hasIssuer, tc.includeSeq)
			if err != nil {
				t.Fatalf("state.SerializeEscrow: %v", err)
			}
			if gotHex := hexUpper(got); gotHex != tc.wantHex {
				t.Fatalf("byte divergence:\n got=%s\nwant=%s", gotHex, tc.wantHex)
			}
		})
	}
}

func hexUpper(b []byte) string {
	const h = "0123456789ABCDEF"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = h[v>>4]
		out[i*2+1] = h[v&0x0f]
	}
	return string(out)
}

func strp(s string) *string { return &s }

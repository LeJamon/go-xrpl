package check

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// Fixed 20-byte account IDs for the golden vectors.
var (
	goldenOwnerID = [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
	goldenDestID  = [20]byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xb4}
)

func u32p(v uint32) *uint32 { return &v }

type checkGoldenCase struct {
	name    string
	tx      *CheckCreate
	sendMax tx.Amount
	ownerNd uint64
	destNd  uint64
	hasDest bool
	wantHex string
}

func checkGoldenCases(t *testing.T) []checkGoldenCase {
	t.Helper()
	iou, err := state.NewIssuedAmountFromDecimalString("123.45", "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	if err != nil {
		t.Fatalf("build IOU: %v", err)
	}

	withExp := &CheckCreate{Expiration: u32p(700000000)}
	withExp.SourceTag = u32p(42)

	full := &CheckCreate{
		DestinationTag: u32p(99),
		Expiration:     u32p(700000000),
		InvoiceID:      "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}
	full.SourceTag = u32p(7)

	return []checkGoldenCase{
		{name: "xrp_minimal", tx: &CheckCreate{}, sendMax: state.NewXRPAmountFromInt(1000000), ownerNd: 3, destNd: 5, hasDest: true,
			wantHex: "1100432200000000240000003725000000003400000000000000033900000000000000055500000000000000000000000000000000000000000000000000000000000000006940000000000F424081140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "xrp_selfsend", tx: &CheckCreate{}, sendMax: state.NewXRPAmountFromInt(2500000), ownerNd: 0, destNd: 0, hasDest: false,
			wantHex: "1100432200000000240000003725000000003400000000000000003900000000000000005500000000000000000000000000000000000000000000000000000000000000006940000000002625A081140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "xrp_tags_exp", tx: withExp, sendMax: state.NewXRPAmountFromInt(9999), ownerNd: 1, destNd: 2, hasDest: true,
			wantHex: "1100432200000000230000002A240000003725000000002A29B9270034000000000000000139000000000000000255000000000000000000000000000000000000000000000000000000000000000069400000000000270F81140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "iou_full", tx: full, sendMax: iou, ownerNd: 7, destNd: 11, hasDest: true,
			wantHex: "11004322000000002300000007240000003725000000002A29B927002E0000006334000000000000000739000000000000000B55000000000000000000000000000000000000000000000000000000000000000050110123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF69D50462C56DF9A8000000000000000000000000005553440000000000B5F762798A53D543A014CAF8B297CFF8F2F937E881140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
	}
}

// TestCheckSerialize_Golden locks the persisted Check SLE bytes produced by the
// creation path (newCheckData → SerializeCheckFromData). These vectors were
// include the required default-zero threading fields present in a fresh rippled SLE.
func TestCheckSerialize_Golden(t *testing.T) {
	for _, tc := range checkGoldenCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			sle := newCheckData(tc.tx, goldenOwnerID, goldenDestID, 55, tc.sendMax)
			sle.OwnerNode = tc.ownerNd
			sle.DestinationNode = tc.destNd
			sle.HasDestNode = tc.hasDest
			got, err := state.SerializeCheckFromData(sle)
			if err != nil {
				t.Fatalf("SerializeCheckFromData: %v", err)
			}
			if gotHex := toUpperHex(got); gotHex != tc.wantHex {
				t.Fatalf("byte divergence:\n got=%s\nwant=%s", gotHex, tc.wantHex)
			}
		})
	}
}

func toUpperHex(b []byte) string {
	const hexdigits = "0123456789ABCDEF"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

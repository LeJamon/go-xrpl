package paychan

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

var (
	pcOwnerID = [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
	pcDestID  = [20]byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xb4}
)

func pcU32(v uint32) *uint32 { return &v }

// TestPayChannelSerialize_Golden locks the persisted PayChannel SLE bytes
// produced by the creation path (newPayChannelData → SerializePayChannelFromData).
// The vectors cover valid creation inputs and lock their persisted bytes.
func TestPayChannelSerialize_Golden(t *testing.T) {
	minimal := &PaymentChannelCreate{
		SettleDelay: 3600,
		PublicKey:   "0388935426E0D08083314842EDFBB2D517BD47699F9A4527318A8E10468C97C052",
	}

	full := &PaymentChannelCreate{
		SettleDelay:    86400,
		CancelAfter:    pcU32(700000000),
		PublicKey:      "0388935426E0D08083314842EDFBB2D517BD47699F9A4527318A8E10468C97C052",
		DestinationTag: pcU32(123),
	}
	full.SourceTag = pcU32(456)

	cases := []struct {
		name    string
		tx      *PaymentChannelCreate
		amount  uint64
		ownerNd uint64
		destNd  uint64
		hasDest bool
		seq     uint32
		hasSeq  bool
		wantHex string
	}{
		{name: "minimal", tx: minimal, amount: 100000000, ownerNd: 2, destNd: 4, hasDest: true,
			wantHex: "1100782200000000202700000E10340000000000000002390000000000000004614000000005F5E10062400000000000000071210388935426E0D08083314842EDFBB2D517BD47699F9A4527318A8E10468C97C05281140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "full_with_seq", tx: full, amount: 250000000, ownerNd: 7, destNd: 9, hasDest: true, seq: 42, hasSeq: true,
			wantHex: "110078220000000023000001C8240000002A2E0000007B202429B9270020270001518034000000000000000739000000000000000961400000000EE6B28062400000000000000071210388935426E0D08083314842EDFBB2D517BD47699F9A4527318A8E10468C97C05281140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
		{name: "no_destnode", tx: minimal, amount: 5000000, ownerNd: 1, hasDest: false,
			wantHex: "1100782200000000202700000E103400000000000000016140000000004C4B4062400000000000000071210388935426E0D08083314842EDFBB2D517BD47699F9A4527318A8E10468C97C05281140102030405060708090A0B0C0D0E0F10111213148314A1A2A3A4A5A6A7A8A9AAABACADAEAFB0B1B2B3B4"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sle := newPayChannelData(tc.tx, pcOwnerID, pcDestID, tc.amount)
			sle.OwnerNode = tc.ownerNd
			sle.DestinationNode = tc.destNd
			sle.HasDestNode = tc.hasDest
			sle.Sequence = tc.seq
			sle.HasSequence = tc.hasSeq
			got, err := state.SerializePayChannelFromData(sle)
			if err != nil {
				t.Fatalf("SerializePayChannelFromData: %v", err)
			}
			if gotHex := strings.ToUpper(hex.EncodeToString(got)); gotHex != tc.wantHex {
				t.Fatalf("byte divergence:\n got=%s\nwant=%s", gotHex, tc.wantHex)
			}
		})
	}
}

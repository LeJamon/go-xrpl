// fixIncludeKeyletFields: the PayChannel and Oracle state serializers must
// round-trip the optional keylet-input fields (sfSequence, sfOracleDocumentID),
// including a zero document id whose emission is gated on presence, not value.
package state

import "testing"

func TestPayChannel_RoundTripsSequence(t *testing.T) {
	in := &PayChannelData{
		Account:       [20]byte{1},
		DestinationID: [20]byte{2},
		Amount:        1000,
		Balance:       0,
		SettleDelay:   100,
		Sequence:      7,
		HasSequence:   true,
	}
	data, err := SerializePayChannelFromData(in)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	out, err := ParsePayChannel(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !out.HasSequence || out.Sequence != 7 {
		t.Fatalf("Sequence not round-tripped: HasSequence=%v Sequence=%d", out.HasSequence, out.Sequence)
	}
}

func TestPayChannel_OmitsSequenceWhenAbsent(t *testing.T) {
	in := &PayChannelData{Account: [20]byte{1}, DestinationID: [20]byte{2}, Amount: 1000, SettleDelay: 100}
	data, err := SerializePayChannelFromData(in)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	out, err := ParsePayChannel(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.HasSequence {
		t.Fatalf("Sequence must be absent when HasSequence is false")
	}
}

func TestOracle_RoundTripsDocumentID(t *testing.T) {
	// A zero id must survive: presence gates emission, not the value.
	for _, id := range []uint32{0, 13} {
		in := &OracleData{
			Owner:               [20]byte{1},
			Provider:            "AB",
			AssetClass:          "CD",
			LastUpdateTime:      100,
			OracleDocumentID:    id,
			HasOracleDocumentID: true,
			PriceDataSeries: []OraclePriceData{
				{BaseAsset: "XRP", QuoteAsset: "USD", AssetPrice: 740, HasPrice: true, Scale: 1, HasScale: true},
			},
		}
		data, err := SerializeOracle(in)
		if err != nil {
			t.Fatalf("serialize id=%d: %v", id, err)
		}
		out, err := ParseOracle(data)
		if err != nil {
			t.Fatalf("parse id=%d: %v", id, err)
		}
		if !out.HasOracleDocumentID || out.OracleDocumentID != id {
			t.Fatalf("OracleDocumentID=%d not round-tripped: Has=%v got=%d", id, out.HasOracleDocumentID, out.OracleDocumentID)
		}
	}
}

func TestOracle_OmitsDocumentIDWhenAbsent(t *testing.T) {
	in := &OracleData{
		Owner:          [20]byte{1},
		Provider:       "AB",
		AssetClass:     "CD",
		LastUpdateTime: 100,
		PriceDataSeries: []OraclePriceData{
			{BaseAsset: "XRP", QuoteAsset: "USD", AssetPrice: 740, HasPrice: true, Scale: 1, HasScale: true},
		},
	}
	data, err := SerializeOracle(in)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	out, err := ParseOracle(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.HasOracleDocumentID {
		t.Fatalf("OracleDocumentID must be absent when HasOracleDocumentID is false")
	}
}

func TestOracle_RoundTripsPresentZeroScale(t *testing.T) {
	in := &OracleData{
		Owner:          [20]byte{1},
		Provider:       "AB",
		AssetClass:     "CD",
		LastUpdateTime: 100,
		PriceDataSeries: []OraclePriceData{
			{BaseAsset: "XRP", QuoteAsset: "USD", Scale: 0, HasScale: true},
		},
	}
	want, err := SerializeOracle(in)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	out, err := ParseOracle(want)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out.PriceDataSeries) != 1 || !out.PriceDataSeries[0].HasScale || out.PriceDataSeries[0].Scale != 0 {
		t.Fatalf("present zero Scale not preserved: %+v", out.PriceDataSeries)
	}
	got, err := SerializeOracle(out)
	if err != nil {
		t.Fatalf("reserialize: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round-trip bytes differ:\nwant %X\n got %X", want, got)
	}
}

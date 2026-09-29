package handlers

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func TestAccountObjectSponsorshipPresence(t *testing.T) {
	for _, tc := range []struct {
		name, kind, field string
		want              bool
	}{
		{"no sponsor", "Check", "", false},
		{"empty sponsor remains present", "Check", "Sponsor", true},
		{"low side", "RippleState", "LowSponsor", true},
		{"high side", "RippleState", "HighSponsor", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{"LedgerEntryType": tc.kind, "Flags": uint32(0), "PreviousTxnID": "0", "PreviousTxnLgrSeq": uint32(0)}
			if tc.kind == "Check" {
				fields["Account"] = ""
				fields["Destination"] = ""
				fields["SendMax"] = "1"
				fields["Sequence"] = uint32(1)
				fields["OwnerNode"] = "0"
				fields["DestinationNode"] = "0"
			} else {
				amount := map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "value": "0"}
				fields["Balance"], fields["LowLimit"], fields["HighLimit"] = amount, amount, amount
			}
			if tc.field != "" {
				fields[tc.field] = ""
			}
			data, err := binarycodec.EncodeBytes(fields)
			require.NoError(t, err)
			got, err := accountObjectIsSponsored(types.AccountObjectItem{LedgerEntryType: tc.kind, Data: data})
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

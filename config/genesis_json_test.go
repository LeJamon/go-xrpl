package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestParseHexFeeUint64Boundaries(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  uint64
	}{
		{name: "single digit", input: "A", want: 10},
		{name: "leading zeroes", input: "000000000000000A", want: 10},
		{name: "maximum", input: "FFFFFFFFFFFFFFFF", want: math.MaxUint64},
		{name: "maximum with leading zeroes", input: "00FFFFFFFFFFFFFFFF", want: math.MaxUint64},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseHexFee(test.input)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	for _, input := range []string{"10000000000000000", "1000000000000000A"} {
		t.Run(input, func(t *testing.T) {
			_, err := parseHexFee(input)
			require.Error(t, err)
			require.ErrorIs(t, err, strconv.ErrRange)
		})
	}
}

func TestGenesisJSONFeeSettingsSerialization(t *testing.T) {
	tests := []struct {
		name         string
		xrpFees      bool
		fees         string
		want         [3]drops.XRPAmount
		wantHex      string
		wantFields   map[string]any
		absentFields []string
	}{
		{
			name: "legacy",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"A",
				"ReferenceFeeUnits":10,
				"ReserveBase":200000,
				"ReserveIncrement":50000
			}`,
			want:    [3]drops.XRPAmount{10, 200_000, 50_000},
			wantHex: "1100732200000000201e0000000a201f00030d4020200000c35035000000000000000a",
			wantFields: map[string]any{
				"BaseFee":           "a",
				"ReferenceFeeUnits": uint32(10),
				"ReserveBase":       uint32(200_000),
				"ReserveIncrement":  uint32(50_000),
			},
			absentFields: []string{"BaseFeeDrops", "ReserveBaseDrops", "ReserveIncrementDrops"},
		},
		{
			name:    "modern",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"10",
				"ReserveBaseDrops":"200000",
				"ReserveIncrementDrops":"50000"
			}`,
			want:    [3]drops.XRPAmount{10, 200_000, 50_000},
			wantHex: "11007322000000006016400000000000000a60174000000000030d406018400000000000c350",
			wantFields: map[string]any{
				"BaseFeeDrops":          "10",
				"ReserveBaseDrops":      "200000",
				"ReserveIncrementDrops": "50000",
			},
			absentFields: []string{"BaseFee", "ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"},
		},
		{
			name:    "modern zero",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"0",
				"ReserveBaseDrops":"0",
				"ReserveIncrementDrops":"0"
			}`,
			want:    [3]drops.XRPAmount{},
			wantHex: "1100732200000000601640000000000000006017400000000000000060184000000000000000",
			wantFields: map[string]any{
				"BaseFeeDrops":          "0",
				"ReserveBaseDrops":      "0",
				"ReserveIncrementDrops": "0",
			},
			absentFields: []string{"BaseFee", "ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := genesisJSONWithFees(test.fees, test.xrpFees)
			require.NoError(t, input.Validate())

			cfg, err := input.ToGenesisConfig()
			require.NoError(t, err)
			require.Equal(t, test.want[0], cfg.BaseFee)
			require.Equal(t, test.want[1], cfg.ReserveBase)
			require.Equal(t, test.want[2], cfg.ReserveIncrement)

			data := createFeeSettingsBytes(t, cfg)
			require.Equal(t, test.wantHex, hex.EncodeToString(data))

			fields, err := binarycodec.DecodeBytes(data)
			require.NoError(t, err)
			for name, want := range test.wantFields {
				require.Equal(t, want, fields[name], name)
			}
			for _, name := range test.absentFields {
				require.NotContains(t, fields, name)
			}

			state, err := input.ParseState()
			require.NoError(t, err)
			roundTripJSON, err := json.Marshal(state.FeeSettings)
			require.NoError(t, err)
			roundTrip := genesisJSONWithFees(string(roundTripJSON), test.xrpFees)
			require.NoError(t, roundTrip.Validate())
			roundTripConfig, err := roundTrip.ToGenesisConfig()
			require.NoError(t, err)
			require.Equal(t, data, createFeeSettingsBytes(t, roundTripConfig))
		})
	}
}

func TestGenesisJSONFeeSettingsValidation(t *testing.T) {
	maxNativePlusOne := uint64(drops.MaxDrops) + 1
	tests := []struct {
		name    string
		xrpFees bool
		fees    string
		wantErr string
	}{
		{
			name:    "incomplete modern base fee",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","ReserveBaseDrops":"1","ReserveIncrementDrops":"1"}`,
			wantErr: "complete modern fee settings",
		},
		{
			name:    "incomplete modern reserve base",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"1","ReserveIncrementDrops":"1"}`,
			wantErr: "complete modern fee settings",
		},
		{
			name:    "incomplete modern reserve increment",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"1","ReserveBaseDrops":"1"}`,
			wantErr: "complete modern fee settings",
		},
		{
			name:    "incomplete legacy",
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFee":"A","ReserveBase":1,"ReserveIncrement":1}`,
			wantErr: "complete legacy fee settings",
		},
		{
			name:    "mixed zero legacy presence",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"1",
				"ReserveBaseDrops":"1",
				"ReserveIncrementDrops":"1",
				"ReferenceFeeUnits":0
			}`,
			wantErr: "mixed legacy and modern fee settings",
		},
		{
			name: "modern without amendment",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"1",
				"ReserveBaseDrops":"1",
				"ReserveIncrementDrops":"1"
			}`,
			wantErr: "require the XRPFees amendment",
		},
		{
			name:    "legacy with amendment",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"A",
				"ReferenceFeeUnits":10,
				"ReserveBase":1,
				"ReserveIncrement":1
			}`,
			wantErr: "complete modern fee settings",
		},
		{
			name:    "malformed modern amount",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"ten",
				"ReserveBaseDrops":"1",
				"ReserveIncrementDrops":"1"
			}`,
			wantErr: "invalid BaseFeeDrops",
		},
		{
			name:    "non-canonical modern amount",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"01",
				"ReserveBaseDrops":"1",
				"ReserveIncrementDrops":"1"
			}`,
			wantErr: "invalid BaseFeeDrops",
		},
		{
			name:    "null modern amount",
			xrpFees: true,
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":null,
				"ReserveBaseDrops":"1",
				"ReserveIncrementDrops":"1"
			}`,
			wantErr: "BaseFeeDrops must not be null",
		},
		{
			name:    "modern amount out of range",
			xrpFees: true,
			fees: fmt.Sprintf(`{
				"LedgerEntryType":"FeeSettings",
				"BaseFeeDrops":"%d",
				"ReserveBaseDrops":"1",
				"ReserveIncrementDrops":"1"
			}`, maxNativePlusOne),
			wantErr: "BaseFeeDrops out of range",
		},
		{
			name:    "negative modern base fee",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"-1","ReserveBaseDrops":"1","ReserveIncrementDrops":"1"}`,
			wantErr: "BaseFeeDrops out of range",
		},
		{
			name:    "negative modern reserve base",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"1","ReserveBaseDrops":"-1","ReserveIncrementDrops":"1"}`,
			wantErr: "ReserveBaseDrops out of range",
		},
		{
			name:    "negative modern reserve increment",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"1","ReserveBaseDrops":"1","ReserveIncrementDrops":"-1"}`,
			wantErr: "ReserveIncrementDrops out of range",
		},
		{
			name:    "unknown field",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"1","ReserveBaseDrops":"1","ReserveIncrementDrops":"1","Typo":1}`,
			wantErr: "unknown FeeSettings field",
		},
		{
			name:    "case variant field",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","baseFeeDrops":"1","ReserveBaseDrops":"1","ReserveIncrementDrops":"1"}`,
			wantErr: "unknown FeeSettings field",
		},
		{
			name: "empty legacy base fee",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"",
				"ReferenceFeeUnits":10,
				"ReserveBase":1,
				"ReserveIncrement":1
			}`,
			wantErr: "invalid BaseFee",
		},
		{
			name: "prefixed legacy base fee",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"0xA",
				"ReferenceFeeUnits":10,
				"ReserveBase":1,
				"ReserveIncrement":1
			}`,
			wantErr: "invalid BaseFee",
		},
		{
			name: "legacy base fee maximum plus one",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"10000000000000000",
				"ReferenceFeeUnits":10,
				"ReserveBase":1,
				"ReserveIncrement":1
			}`,
			wantErr: "invalid BaseFee",
		},
		{
			name: "legacy base fee overflow with low bits",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"1000000000000000A",
				"ReferenceFeeUnits":10,
				"ReserveBase":1,
				"ReserveIncrement":1
			}`,
			wantErr: "invalid BaseFee",
		},
		{
			name: "legacy base fee out of range",
			fees: fmt.Sprintf(`{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"%x",
				"ReferenceFeeUnits":10,
				"ReserveBase":1,
				"ReserveIncrement":1
			}`, maxNativePlusOne),
			wantErr: "BaseFee out of range",
		},
		{
			name: "legacy reserve out of range",
			fees: `{
				"LedgerEntryType":"FeeSettings",
				"BaseFee":"A",
				"ReferenceFeeUnits":10,
				"ReserveBase":4294967296,
				"ReserveIncrement":1
			}`,
			wantErr: "ReserveBase out of range",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := genesisJSONWithFees(test.fees, test.xrpFees)
			require.ErrorContains(t, input.Validate(), test.wantErr)
			_, err := input.ToGenesisConfig()
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestGenesisJSONFeeSettingsRippledInputForms(t *testing.T) {
	tests := []struct {
		name    string
		xrpFees bool
		fees    string
		want    [3]drops.XRPAmount
	}{
		{
			name:    "numeric modern amounts",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":10,"ReserveBaseDrops":200000,"ReserveIncrementDrops":50000}`,
			want:    [3]drops.XRPAmount{10, 200_000, 50_000},
		},
		{
			name:    "integral exponent modern amounts",
			xrpFees: true,
			fees:    `{"LedgerEntryType":"FeeSettings","BaseFeeDrops":"+1e1","ReserveBaseDrops":"2e5","ReserveIncrementDrops":"5.0e4"}`,
			want:    [3]drops.XRPAmount{10, 200_000, 50_000},
		},
		{
			name: "numeric base fee and string legacy integers",
			fees: `{"LedgerEntryType":"FeeSettings","BaseFee":10,"ReferenceFeeUnits":"10","ReserveBase":"200000","ReserveIncrement":"50000"}`,
			want: [3]drops.XRPAmount{10, 200_000, 50_000},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := genesisJSONWithFees(test.fees, test.xrpFees)
			require.NoError(t, input.Validate())
			cfg, err := input.ToGenesisConfig()
			require.NoError(t, err)
			require.Equal(t, test.want[0], cfg.BaseFee)
			require.Equal(t, test.want[1], cfg.ReserveBase)
			require.Equal(t, test.want[2], cfg.ReserveIncrement)
		})
	}
}

func TestGenesisJSONRejectsLegacyBaseFeeAboveNativeLimit(t *testing.T) {
	baseFee := uint64(drops.MaxDrops) + 1
	input := genesisJSONWithFees(fmt.Sprintf(`{
		"LedgerEntryType":"FeeSettings",
		"BaseFee":"%x",
		"ReferenceFeeUnits":10,
		"ReserveBase":1,
		"ReserveIncrement":1
	}`, baseFee), false)
	require.ErrorContains(t, input.Validate(), "BaseFee out of range")
	_, err := input.ToGenesisConfig()
	require.ErrorContains(t, err, "BaseFee out of range")
}

func TestFeeSettingsJSONMarshalProgrammaticLegacy(t *testing.T) {
	settings := FeeSettingsJSON{
		LedgerEntryType:   "FeeSettings",
		BaseFee:           "A",
		ReferenceFeeUnits: 10,
		ReserveBase:       200_000,
		ReserveIncrement:  50_000,
	}
	data, err := json.Marshal(settings)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	for _, name := range []string{"BaseFee", "ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"} {
		require.Contains(t, fields, name)
	}
	for _, name := range []string{"BaseFeeDrops", "ReserveBaseDrops", "ReserveIncrementDrops"} {
		require.NotContains(t, fields, name)
	}
	require.NoError(t, genesisJSONWithFees(string(data), false).Validate())
}

func TestFeeSettingsJSONPreservesOptionalFields(t *testing.T) {
	input := `{
		"LedgerEntryType":"FeeSettings",
		"Flags":0,
		"BaseFee":"A",
		"ReferenceFeeUnits":10,
		"ReserveBase":200000,
		"ReserveIncrement":50000,
		"LedgerIndex":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"Sponsor":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"PreviousTxnID":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"PreviousTxnLgrSeq":0
	}`
	var settings FeeSettingsJSON
	require.NoError(t, json.Unmarshal([]byte(input), &settings))

	data, err := json.Marshal(settings)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Equal(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", fields["LedgerIndex"])
	require.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", fields["Sponsor"])
	require.Equal(t, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", fields["PreviousTxnID"])
	require.Equal(t, float64(0), fields["PreviousTxnLgrSeq"])
}

func genesisJSONWithFees(fees string, xrpFees bool) *GenesisJSON {
	accountState := []json.RawMessage{json.RawMessage(fees)}
	if xrpFees {
		accountState = append(accountState, json.RawMessage(fmt.Sprintf(
			`{"LedgerEntryType":"Amendments","Amendments":["%s"]}`,
			hex.EncodeToString(amendment.FeatureXRPFees[:]),
		)))
	}
	return &GenesisJSON{Ledger: GenesisLedgerJSON{AccountState: accountState}}
}

func createFeeSettingsBytes(t *testing.T, cfg *GenesisConfig) []byte {
	t.Helper()
	ledger, err := genesis.Create(genesis.Config{
		TotalXRP:            cfg.TotalXRP,
		CloseTimeResolution: cfg.CloseTimeResolution,
		Fees: genesis.DefaultFees{
			BaseFee:          cfg.BaseFee,
			ReserveBase:      cfg.ReserveBase,
			ReserveIncrement: cfg.ReserveIncrement,
		},
		Amendments: cfg.Amendments,
	})
	require.NoError(t, err)
	item, found, err := ledger.StateMap.Get(keylet.Fees().Key)
	require.NoError(t, err)
	require.True(t, found)
	return item.Data()
}

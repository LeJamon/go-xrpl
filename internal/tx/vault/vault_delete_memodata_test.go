package vault

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestLendingProtocolV1_1Registration(t *testing.T) {
	const wantID = "A360E2BFD775A5B0DCE1C36C16DF31B72735A57584FD163655D2F9564F8E7AC8"

	feature := amendment.FeatureByName("LendingProtocolV1_1")
	if feature == nil {
		t.Fatal("LendingProtocolV1_1 is not registered")
	}
	if got := strings.ToUpper(hex.EncodeToString(feature.ID[:])); got != wantID {
		t.Fatalf("LendingProtocolV1_1 ID = %s, want %s", got, wantID)
	}
	if feature.Supported != amendment.SupportedNo {
		t.Fatalf("LendingProtocolV1_1 support = %v, want SupportedNo", feature.Supported)
	}
	if feature.Vote != amendment.VoteDefaultNo {
		t.Fatalf("LendingProtocolV1_1 vote = %v, want VoteDefaultNo", feature.Vote)
	}
	if amendment.AllSupportedRules().Enabled(feature.ID) {
		t.Fatal("unsupported LendingProtocolV1_1 must not be enabled by AllSupportedRules")
	}
}

func TestVaultDeleteMemoDataPreflight(t *testing.T) {
	off := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	on := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureLendingProtocolV1_1,
	})
	validVaultID := strings.Repeat("01", 32)

	tests := []struct {
		name      string
		vaultID   string
		memoData  string
		present   bool
		rules     *amendment.Rules
		want      ter.Result
		wantError bool
	}{
		{name: "absent amendment off", vaultID: validVaultID, rules: off},
		{name: "odd length amendment on", vaultID: validVaultID, memoData: "A", rules: on},
		{name: "one byte amendment on", vaultID: validVaultID, memoData: "AA", rules: on},
		{name: "maximum amendment on", vaultID: validVaultID, memoData: strings.Repeat("AA", MaxVaultDataLength), rules: on},
		{name: "empty amendment on", vaultID: validVaultID, present: true, rules: on, want: ter.TemMALFORMED, wantError: true},
		{name: "oversized amendment on", vaultID: validVaultID, memoData: strings.Repeat("AA", MaxVaultDataLength+1), rules: on, want: ter.TemMALFORMED, wantError: true},
		{name: "malformed hex amendment on", vaultID: validVaultID, memoData: "GG", rules: on, want: ter.TemMALFORMED, wantError: true},
		{name: "empty amendment off", vaultID: validVaultID, present: true, rules: off, want: ter.TemDISABLED, wantError: true},
		{name: "oversized amendment off", vaultID: validVaultID, memoData: strings.Repeat("AA", MaxVaultDataLength+1), rules: off, want: ter.TemDISABLED, wantError: true},
		{name: "zero VaultID precedes amendment gate", vaultID: strings.Repeat("00", 32), memoData: "AA", rules: off, want: ter.TemMALFORMED, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deleteTx := NewVaultDelete("rOwner", test.vaultID)
			deleteTx.MemoData = test.memoData
			if test.present {
				deleteTx.Common.SetPresentFields(map[string]bool{"MemoData": true})
			}

			err := deleteTx.PreflightWithRules(test.rules)
			if !test.wantError {
				if err != nil {
					t.Fatalf("PreflightWithRules() = %v, want nil", err)
				}
				return
			}
			if got := vaultResultCode(t, err); got != test.want {
				t.Fatalf("PreflightWithRules() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVaultDeleteMemoDataCodecRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		decoded string
		present bool
	}{
		{name: "absent"},
		{name: "empty", present: true},
		{name: "odd length", value: "A", decoded: "0A", present: true},
		{name: "one byte", value: "AB", present: true},
		{name: "maximum", value: strings.Repeat("AB", MaxVaultDataLength), present: true},
		{name: "oversized", value: strings.Repeat("AB", MaxVaultDataLength+1), present: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]any{
				"TransactionType": "VaultDelete",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Sequence":        uint32(1),
				"Fee":             "10",
				"SigningPubKey":   "",
				"VaultID":         strings.Repeat("01", 32),
			}
			if test.present {
				fields["MemoData"] = test.value
			}

			encoded, err := binarycodec.Encode(fields)
			if err != nil {
				t.Fatalf("encode VaultDelete: %v", err)
			}
			blob, err := hex.DecodeString(encoded)
			if err != nil {
				t.Fatalf("decode encoded transaction: %v", err)
			}
			parsed, err := tx.ParseFromBinary(blob)
			if err != nil {
				t.Fatalf("parse VaultDelete: %v", err)
			}
			deleteTx, ok := parsed.(*VaultDelete)
			if !ok {
				t.Fatalf("parsed transaction = %T, want *VaultDelete", parsed)
			}
			wantValue := test.value
			if test.decoded != "" {
				wantValue = test.decoded
			}
			if deleteTx.MemoData != wantValue {
				t.Fatalf("MemoData = %q, want %q", deleteTx.MemoData, wantValue)
			}
			if got := deleteTx.Common.HasField("MemoData"); got != test.present {
				t.Fatalf("MemoData presence = %v, want %v", got, test.present)
			}

			flat, err := deleteTx.Flatten()
			if err != nil {
				t.Fatalf("flatten VaultDelete: %v", err)
			}
			flattened, flattenedPresent := flat["MemoData"]
			if flattenedPresent != test.present || test.present && flattened != wantValue {
				t.Fatalf("flattened MemoData = %#v (present %v), want %q (present %v)", flattened, flattenedPresent, wantValue, test.present)
			}
		})
	}
}

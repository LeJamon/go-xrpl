package vault

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestVaultCreateRejectsPresentEmptyBlobs(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  error
	}{
		{name: "Data", field: "Data", want: ErrVaultDataEmpty},
		{name: "MPTokenMetadata", field: "MPTokenMetadata", want: ErrVaultMetadataEmpty},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			create := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
			create.Common.SetPresentFields(map[string]bool{test.field: true})
			if got := create.Validate(); got != test.want {
				t.Fatalf("Validate() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVaultJSONParsingPreservesPresentEmptyFields(t *testing.T) {
	Register()
	parsed, err := tx.ParseJSON([]byte(`{
		"TransactionType":"VaultCreate",
		"Account":"rOwner",
		"Asset":{"currency":"XRP"},
		"Data":""
	}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	create, ok := parsed.(*VaultCreate)
	if !ok {
		t.Fatalf("ParseJSON returned %T, want *VaultCreate", parsed)
	}
	if !create.Common.HasField("Data") {
		t.Fatal("ParseJSON lost Data field presence")
	}
	if got := create.Validate(); got != ErrVaultDataEmpty {
		t.Fatalf("Validate() = %v, want %v", got, ErrVaultDataEmpty)
	}

	for _, transactionType := range []string{"VaultCreate", "VaultSet"} {
		t.Run(transactionType+" rejects malformed AssetsMaximum", func(t *testing.T) {
			_, err := tx.ParseJSON([]byte(`{
				"TransactionType":"` + transactionType + `",
				"Account":"rOwner",
				"Asset":{"currency":"XRP"},
				"VaultID":"0101010101010101010101010101010101010101010101010101010101010101",
				"AssetsMaximum":"9223372036854775807.6"
			}`))
			if err == nil {
				t.Fatal("ParseJSON accepted malformed AssetsMaximum")
			}
		})
	}
}

func TestVaultCreateRejectsMalformedProgrammaticAssetsMaximum(t *testing.T) {
	maximum := "not-a-number"
	create := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
	create.AssetsMaximum = &maximum
	if got := vaultResultCode(t, create.Validate()); got != ter.TemMALFORMED {
		t.Fatalf("Validate() = %v, want temMALFORMED", got)
	}
}

func TestVaultCreateUsesNormalBaseFee(t *testing.T) {
	var transaction tx.Transaction = NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
	if _, ok := transaction.(tx.CustomBaseFeeCalculator); ok {
		t.Fatal("VaultCreate unexpectedly overrides the normal base fee")
	}
}

func TestVaultCreateAcceptsNegativeZeroAssetsMaximum(t *testing.T) {
	maximum := "-0"
	create := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
	create.AssetsMaximum = &maximum
	if err := create.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestVaultCreateScaleValidation(t *testing.T) {
	zero := uint8(0)
	one := uint8(1)
	maximum := uint8(18)
	overMaximum := uint8(19)
	maximumUint8 := uint8(255)

	tests := []struct {
		name  string
		asset tx.Asset
		scale *uint8
		want  error
	}{
		{name: "IOU omitted", asset: tx.Asset{Currency: "USD", Issuer: "rIssuer"}},
		{name: "IOU zero", asset: tx.Asset{Currency: "USD", Issuer: "rIssuer"}, scale: &zero},
		{name: "IOU maximum", asset: tx.Asset{Currency: "USD", Issuer: "rIssuer"}, scale: &maximum},
		{name: "IOU over maximum", asset: tx.Asset{Currency: "USD", Issuer: "rIssuer"}, scale: &overMaximum, want: ErrVaultScaleTooLarge},
		{name: "IOU uint8 maximum", asset: tx.Asset{Currency: "USD", Issuer: "rIssuer"}, scale: &maximumUint8, want: ErrVaultScaleTooLarge},
		{name: "XRP zero", asset: tx.Asset{Currency: "XRP"}, scale: &zero, want: ErrVaultScaleForbidden},
		{name: "XRP one", asset: tx.Asset{Currency: "XRP"}, scale: &one, want: ErrVaultScaleForbidden},
		{name: "MPT zero", asset: tx.Asset{MPTIssuanceID: "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"}, scale: &zero, want: ErrVaultScaleForbidden},
		{name: "MPT one", asset: tx.Asset{MPTIssuanceID: "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"}, scale: &one, want: ErrVaultScaleForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			create := NewVaultCreate("rOwner", test.asset)
			create.Scale = test.scale
			if got := create.Validate(); got != test.want {
				t.Fatalf("Validate() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVaultCreatePresentEmptyDomainIsGatedAndMalformed(t *testing.T) {
	create := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
	create.Common.SetPresentFields(map[string]bool{"DomainID": true})

	if got := vaultResultCode(t, create.Validate()); got != ter.TemMALFORMED {
		t.Fatalf("Validate() = %v, want temMALFORMED", got)
	}
	for _, feature := range create.RequiredAmendments() {
		if feature == amendment.FeaturePermissionedDomains {
			return
		}
	}
	t.Fatal("RequiredAmendments() omitted PermissionedDomains")
}

func TestVaultCreateAssetsMaximumMatchesAssetPrecision(t *testing.T) {
	xrp := tx.Asset{Currency: "XRP"}
	mpt := tx.Asset{MPTIssuanceID: "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"}
	iou := tx.Asset{Currency: "USD", Issuer: "rIssuer"}

	tests := []struct {
		name  string
		asset tx.Asset
		raw   string
		want  string
	}{
		{name: "XRP network maximum", asset: xrp, raw: "100000000000000000", want: "100000000000000000e0"},
		{name: "XRP rounds fractional drops", asset: xrp, raw: "9223372036854775.808", want: "9223372036854776e0"},
		{name: "MPT signed int64 maximum", asset: mpt, raw: "9223372036854775807", want: "9223372036854775807e0"},
		{name: "MPT signed int64 maximum plus one", asset: mpt, raw: "9223372036854775808", want: "9223372036854775807e0"},
		{name: "MPT XRP maximum plus one", asset: mpt, raw: "100000000000000001", want: "100000000000000001e0"},
		{name: "MPT XRP maximum", asset: mpt, raw: "100000000000000000", want: "100000000000000000e0"},
		{name: "MPT rounds fractional units", asset: mpt, raw: "922337203685477580.8", want: "922337203685477581e0"},
		{name: "IOU signed int64 maximum", asset: iou, raw: "9223372036854775807", want: "9223372036854776e3"},
		{name: "IOU XRP maximum plus one", asset: iou, raw: "100000000000000001", want: "1000000000000000e2"},
		{name: "IOU XRP maximum", asset: iou, raw: "100000000000000000", want: "1000000000000000e2"},
		{name: "IOU signed int64 maximum plus one", asset: iou, raw: "9223372036854775808", want: "9223372036854776e3"},
		{name: "IOU maximum exponent", asset: iou, raw: "1000000000000000e80", want: "1000000000000000e80"},
		{name: "IOU minimum exponent", asset: iou, raw: "1000000000000000e-96", want: "1000000000000000e-96"},
		{name: "IOU rounds to sixteen digits", asset: iou, raw: "922337203685477580.8", want: "9223372036854776e2"},
		{name: "IOU large exponent", asset: iou, raw: "9223372036854775807e40", want: "9223372036854776e43"},
		{name: "IOU small exponent", asset: iou, raw: "9223372036854775807e-40", want: "9223372036854776e-37"},
		{name: "IOU underflow removes default", asset: iou, raw: "9223372036854775807e-100", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			number, err := parseCreateSetNumber(test.raw)
			if err != nil {
				t.Fatalf("parseCreateSetNumber(%q): %v", test.raw, err)
			}
			got := canonicalCreateSetAssetsMaximum(test.asset, number)
			if test.want == "" {
				if got != "" {
					t.Fatalf("canonical AssetsMaximum = %q, want default removal", got)
				}
				return
			}
			gotNumber, err := parseCreateSetNumber(got)
			if err != nil {
				t.Fatalf("parse canonical AssetsMaximum %q: %v", got, err)
			}
			wantNumber, err := parseCreateSetNumber(test.want)
			if err != nil {
				t.Fatalf("parse expected AssetsMaximum %q: %v", test.want, err)
			}
			if gotNumber.Cmp(wantNumber) != 0 {
				t.Fatalf("canonical AssetsMaximum = %q, want value %q", got, test.want)
			}
		})
	}

	panicTests := []struct {
		name  string
		asset tx.Asset
		raw   string
	}{
		{name: "XRP above network maximum", asset: xrp, raw: "100000000000000001"},
		{name: "XRP int64 maximum", asset: xrp, raw: "9223372036854775807"},
		{name: "MPT above rounded int64 maximum", asset: mpt, raw: "9223372036854775809"},
		{name: "IOU above maximum exponent", asset: iou, raw: "1000000000000000e81"},
	}
	for _, test := range panicTests {
		t.Run(test.name, func(t *testing.T) {
			number, err := parseCreateSetNumber(test.raw)
			if err != nil {
				t.Fatalf("parseCreateSetNumber(%q): %v", test.raw, err)
			}
			defer func() {
				if recover() == nil {
					t.Fatalf("canonical AssetsMaximum did not panic")
				}
			}()
			canonicalCreateSetAssetsMaximum(test.asset, number)
		})
	}
}

func TestVaultCreateApplyRemovesRoundedZeroAssetsMaximum(t *testing.T) {
	view := newMPTArmsView()
	var ownerID [20]byte
	for i := range ownerID {
		ownerID[i] = 1
	}
	ctx := buildArmsCtx(t, view, ownerID, rulesWithFix(true))
	sequence := uint32(1)
	maximum := "0.4"
	create := NewVaultCreate(ctx.Account.Account, tx.Asset{Currency: "XRP"})
	create.Common.Sequence = &sequence
	create.AssetsMaximum = &maximum

	if got := create.Apply(ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply() = %v, want tesSUCCESS", got)
	}
	created, err := readVault(view, keylet.Vault(ownerID, sequence))
	if err != nil {
		t.Fatalf("read created vault: %v", err)
	}
	if created == nil {
		t.Fatal("created vault is missing")
	}
	if created.AssetsMaximum != "" {
		t.Fatalf("AssetsMaximum = %q, want default removal", created.AssetsMaximum)
	}
}

func TestVaultNumberSerializationPreservesLargeMantissa(t *testing.T) {
	encoded, err := encodeVaultNumber("9223372036854775807e0", state.MantissaScaleLarge)
	if err != nil {
		t.Fatalf("encodeVaultNumber: %v", err)
	}
	if got := int64(binary.BigEndian.Uint64(encoded[:8])); got != math.MaxInt64 {
		t.Fatalf("encoded mantissa = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := int32(binary.BigEndian.Uint32(encoded[8:])); got != 0 {
		t.Fatalf("encoded exponent = %d, want 0", got)
	}
}

func TestSerializeVaultPreservesLargeAssetsMaximum(t *testing.T) {
	data := &vaultData{
		Owner:            [20]byte{1},
		Account:          [20]byte{2},
		Sequence:         1,
		ShareMPTID:       [24]byte{3},
		Asset:            tx.Asset{Currency: "XRP"},
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
		AssetsMaximum:    "9223372036854775807e0",
	}
	encoded, err := serializeVault(data)
	if err != nil {
		t.Fatalf("serializeVault: %v", err)
	}
	fields, err := readVaultNumberFields(encoded)
	if err != nil {
		t.Fatalf("readVaultNumberFields: %v", err)
	}
	if got := fields["AssetsMaximum"]; got != data.AssetsMaximum {
		t.Fatalf("AssetsMaximum = %q, want %q", got, data.AssetsMaximum)
	}
}

func vaultResultCode(t *testing.T, err error) ter.Result {
	t.Helper()
	result, ok := ter.AsResultError(err)
	if !ok {
		t.Fatalf("error %v does not carry a TER", err)
	}
	return result.Code
}

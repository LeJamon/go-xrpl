package credential

import (
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestCredentialTypeBuildersUseExplicitRepresentations(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	type builderCase struct {
		name  string
		text  func(string) (string, error)
		bytes func([]byte) (string, error)
		hex   func(string) (string, error)
	}

	builders := []builderCase{
		{
			name: "create",
			text: func(value string) (string, error) {
				built := CredentialCreateText(issuer, subject, value).Build()
				return built.CredentialType, built.Validate()
			},
			bytes: func(value []byte) (string, error) {
				built := CredentialCreateBytes(issuer, subject, value).Build()
				return built.CredentialType, built.Validate()
			},
			hex: func(value string) (string, error) {
				built := CredentialCreateHex(issuer, subject, value).Build()
				return built.CredentialType, built.Validate()
			},
		},
		{
			name: "accept",
			text: func(value string) (string, error) {
				built := CredentialAcceptText(subject, issuer, value).Build()
				return built.CredentialType, built.Validate()
			},
			bytes: func(value []byte) (string, error) {
				built := CredentialAcceptBytes(subject, issuer, value).Build()
				return built.CredentialType, built.Validate()
			},
			hex: func(value string) (string, error) {
				built := CredentialAcceptHex(subject, issuer, value).Build()
				return built.CredentialType, built.Validate()
			},
		},
		{
			name: "delete",
			text: func(value string) (string, error) {
				built := CredentialDeleteText(subject, subject, issuer, value).Build()
				return built.CredentialType, built.Validate()
			},
			bytes: func(value []byte) (string, error) {
				built := CredentialDeleteBytes(subject, subject, issuer, value).Build()
				return built.CredentialType, built.Validate()
			},
			hex: func(value string) (string, error) {
				built := CredentialDeleteHex(subject, subject, issuer, value).Build()
				return built.CredentialType, built.Validate()
			},
		},
	}

	textCases := []struct {
		name  string
		input string
		want  string
		err   bool
	}{
		{name: "empty", input: "", want: "", err: true},
		{name: "even hex-looking ASCII AB", input: "AB", want: "4142"},
		{name: "even hex-looking ASCII face", input: "face", want: "66616365"},
		{name: "even hex-looking ASCII deadbeef", input: "deadbeef", want: "6465616462656566"},
		{name: "odd hex-looking ASCII", input: "abc", want: "616263"},
		{name: "invalid hex-looking ASCII", input: "not-hex", want: "6e6f742d686578"},
	}

	hexCases := []struct {
		name  string
		input string
		want  string
		err   bool
	}{
		{name: "empty", input: "", want: "", err: true},
		{name: "even hex AB", input: "AB", want: "AB"},
		{name: "even hex face", input: "face", want: "face"},
		{name: "even hex deadbeef", input: "deadbeef", want: "deadbeef"},
		{name: "uppercase explicit hex", input: "DEADBEEF", want: "DEADBEEF"},
		{name: "odd explicit hex", input: "abc", want: "abc", err: true},
		{name: "invalid explicit hex", input: "not-hex", want: "not-hex", err: true},
	}

	for _, builder := range builders {
		t.Run(builder.name+"/text", func(t *testing.T) {
			for _, tc := range textCases {
				t.Run(tc.name, func(t *testing.T) {
					got, err := builder.text(tc.input)
					require.Equal(t, tc.want, got)
					if tc.err {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				})
			}
		})

		t.Run(builder.name+"/bytes", func(t *testing.T) {
			for _, tc := range textCases {
				t.Run(tc.name, func(t *testing.T) {
					got, err := builder.bytes([]byte(tc.input))
					require.Equal(t, tc.want, got)
					if tc.err {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				})
			}
		})

		t.Run(builder.name+"/hex", func(t *testing.T) {
			for _, tc := range hexCases {
				t.Run(tc.name, func(t *testing.T) {
					got, err := builder.hex(tc.input)
					require.Equal(t, tc.want, got)
					if tc.err {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				})
			}
		})
	}
}

func TestCredentialCreateURIRepresentations(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	type uriCase struct {
		name    string
		apply   func(*CredentialCreateBuilder) *CredentialCreateBuilder
		want    string
		valid   bool
		present bool
	}

	cases := []uriCase{
		{name: "raw empty", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URI("") }, want: "", present: true},
		{name: "raw even hex-looking ASCII AB", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URI("AB") }, want: "4142", valid: true, present: true},
		{name: "raw even hex-looking face", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URI("face") }, want: "66616365", valid: true, present: true},
		{name: "raw even hex-looking deadbeef", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URI("deadbeef") }, want: "6465616462656566", valid: true, present: true},
		{name: "raw odd hex-looking ASCII", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URI("abc") }, want: "616263", valid: true, present: true},
		{name: "raw invalid hex-looking ASCII", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URI("not-hex") }, want: "6e6f742d686578", valid: true, present: true},
		{name: "explicit empty", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("") }, want: "", present: true},
		{name: "explicit even hex AB", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("AB") }, want: "AB", valid: true, present: true},
		{name: "explicit even hex face", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("face") }, want: "face", valid: true, present: true},
		{name: "explicit even hex deadbeef", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("deadbeef") }, want: "deadbeef", valid: true, present: true},
		{name: "explicit uppercase hex", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("DEADBEEF") }, want: "DEADBEEF", valid: true, present: true},
		{name: "explicit odd hex", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("abc") }, want: "abc", present: true},
		{name: "explicit invalid hex", apply: func(b *CredentialCreateBuilder) *CredentialCreateBuilder { return b.URIHex("not-hex") }, want: "not-hex", present: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			built := tc.apply(CredentialCreateText(issuer, subject, "credential")).Build()
			require.Equal(t, tc.want, built.URI)
			require.Equal(t, tc.present, built.HasField("URI"))
			if tc.valid {
				require.NoError(t, built.Validate())
			} else {
				require.Error(t, built.Validate())
			}
		})
	}

	omitted := CredentialCreateText(issuer, subject, "credential").Build()
	require.Empty(t, omitted.URI)
	require.False(t, omitted.HasField("URI"))
}

func TestCredentialBuildersFlattenAndWireFields(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	type flattenCase struct {
		name    string
		build   func() (map[string]any, error)
		flatten map[string]any
		wire    map[string]any
	}

	cases := []flattenCase{
		{
			name: "create with raw URI",
			build: func() (map[string]any, error) {
				built := CredentialCreateText(issuer, subject, "AB").URI("AB").Expiration(42).Fee(123).Flags(7).Build()
				return built.Flatten()
			},
			flatten: map[string]any{
				"Account":         issuer.Address,
				"TransactionType": "CredentialCreate",
				"Fee":             "123",
				"Flags":           uint32(7),
				"Subject":         subject.Address,
				"CredentialType":  "4142",
				"Expiration":      uint32(42),
				"URI":             "4142",
			},
			wire: map[string]any{
				"Account":         issuer.Address,
				"TransactionType": "CredentialCreate",
				"Fee":             "123",
				"Flags":           uint32(7),
				"Subject":         subject.Address,
				"CredentialType":  "4142",
				"Expiration":      uint32(42),
				"URI":             "4142",
			},
		},
		{
			name: "create with explicit URI hex",
			build: func() (map[string]any, error) {
				built := CredentialCreateHex(issuer, subject, "face").URIHex("FACE").Build()
				return built.Flatten()
			},
			flatten: map[string]any{
				"Account":         issuer.Address,
				"TransactionType": "CredentialCreate",
				"Fee":             "10",
				"Subject":         subject.Address,
				"CredentialType":  "face",
				"URI":             "FACE",
			},
			wire: map[string]any{
				"Account":         issuer.Address,
				"TransactionType": "CredentialCreate",
				"Fee":             "10",
				"Subject":         subject.Address,
				"CredentialType":  "FACE",
				"URI":             "FACE",
			},
		},
		{
			name: "accept",
			build: func() (map[string]any, error) {
				built := CredentialAcceptHex(subject, issuer, "AB").Fee(456).Flags(9).Build()
				return built.Flatten()
			},
			flatten: map[string]any{
				"Account":         subject.Address,
				"TransactionType": "CredentialAccept",
				"Fee":             "456",
				"Flags":           uint32(9),
				"Issuer":          issuer.Address,
				"CredentialType":  "AB",
			},
			wire: map[string]any{
				"Account":         subject.Address,
				"TransactionType": "CredentialAccept",
				"Fee":             "456",
				"Flags":           uint32(9),
				"Issuer":          issuer.Address,
				"CredentialType":  "AB",
			},
		},
		{
			name: "delete",
			build: func() (map[string]any, error) {
				built := CredentialDeleteBytes(subject, subject, issuer, []byte{0xab, 0xcd}).Flags(11).Build()
				return built.Flatten()
			},
			flatten: map[string]any{
				"Account":         subject.Address,
				"TransactionType": "CredentialDelete",
				"Fee":             "10",
				"Flags":           uint32(11),
				"Subject":         subject.Address,
				"Issuer":          issuer.Address,
				"CredentialType":  "abcd",
			},
			wire: map[string]any{
				"Account":         subject.Address,
				"TransactionType": "CredentialDelete",
				"Fee":             "10",
				"Flags":           uint32(11),
				"Subject":         subject.Address,
				"Issuer":          issuer.Address,
				"CredentialType":  "ABCD",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flattened, err := tc.build()
			require.NoError(t, err)
			require.Equal(t, tc.flatten, flattened)

			encoded, err := binarycodec.Encode(flattened)
			require.NoError(t, err)
			decoded, err := binarycodec.Decode(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.wire, decoded)
		})
	}
}

func TestCredentialTextBuilderLengthValidation(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	valid := CredentialCreateText(issuer, subject, strings.Repeat("x", 64)).Build()
	require.NoError(t, valid.Validate())

	tooLong := CredentialCreateText(issuer, subject, strings.Repeat("x", 65)).Build()
	require.Error(t, tooLong.Validate())

	require.Equal(t, hex.EncodeToString([]byte("AB")), CredentialCreateText(issuer, subject, "AB").Build().CredentialType)
}

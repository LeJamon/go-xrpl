package binarycodec

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	currencyA     = "0000000000000000000000003078410000000000"
	currency1     = "0000000000000000000000003078310000000000"
	noCurrency    = "0000000000000000000000000000000000000001"
	badCurrency   = "0000000000000000000000005852500000000000"
	issuerAddress = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	issuerBytes   = "AE123A8556F3CF91154711376AFB0F894F832B3D"
	doorAddress   = "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p"
	doorBytes     = "53DF81C37F46159242F7CA9163E09F044029CC12"
	xrpCurrency   = "0000000000000000000000000000000000000000"
)

func TestEncodeCurrencyGrammar_PublicBytes(t *testing.T) {
	currencies := []struct {
		name string
		text string
		wire string
	}{
		{name: "0xA", text: "0xA", wire: currencyA},
		{name: "0x1", text: "0x1", wire: currency1},
	}
	contexts := []struct {
		name   string
		object func(string) map[string]any
		want   func(string) string
	}{
		{
			name:   "bare Currency",
			object: func(currency string) map[string]any { return map[string]any{"BaseAsset": currency} },
			want:   func(currency string) string { return "011A" + currency },
		},
		{
			name: "Issue",
			object: func(currency string) map[string]any {
				return map[string]any{"Asset": map[string]any{"currency": currency, "issuer": issuerAddress}}
			},
			want: func(currency string) string { return "0318" + currency + issuerBytes },
		},
		{
			name: "Amount with raw zero value",
			object: func(currency string) map[string]any {
				return map[string]any{"Amount": map[string]any{"value": "0", "currency": currency, "issuer": issuerAddress}}
			},
			want: func(currency string) string { return "618000000000000000" + currency + issuerBytes },
		},
		{
			name: "PathSet",
			object: func(currency string) map[string]any {
				return map[string]any{"Paths": []any{[]any{map[string]any{"currency": currency}}}}
			},
			want: func(currency string) string { return "011210" + currency + "00" },
		},
		{
			name: "nested XChainBridge",
			object: func(currency string) map[string]any {
				return map[string]any{
					"XChainBridge": map[string]any{
						"LockingChainDoor":  doorAddress,
						"LockingChainIssue": map[string]any{"currency": "XRP"},
						"IssuingChainDoor":  doorAddress,
						"IssuingChainIssue": map[string]any{"currency": currency, "issuer": issuerAddress},
					},
				}
			},
			want: func(currency string) string {
				return "011914" + doorBytes + xrpCurrency + "14" + doorBytes + currency + issuerBytes
			},
		},
	}

	for _, currency := range currencies {
		for _, context := range contexts {
			t.Run(context.name+"/"+currency.name, func(t *testing.T) {
				object := context.object(currency.text)
				want := context.want(currency.wire)
				encoded, err := Encode(object)
				require.NoError(t, err)
				require.Equal(t, want, encoded)

				encodedBytes, err := EncodeBytes(object)
				require.NoError(t, err)
				require.Equal(t, want, strings.ToUpper(hex.EncodeToString(encodedBytes)))
			})
		}
	}
}

func TestEncodeCurrencyGrammar_StrictRejections(t *testing.T) {
	contexts := []struct {
		name  string
		value func(any) map[string]any
	}{
		{
			name:  "bare Currency",
			value: func(currency any) map[string]any { return map[string]any{"BaseAsset": currency} },
		},
		{
			name: "Issue",
			value: func(currency any) map[string]any {
				return map[string]any{"Asset": map[string]any{"currency": currency, "issuer": issuerAddress}}
			},
		},
		{
			name: "Amount",
			value: func(currency any) map[string]any {
				return map[string]any{"Amount": map[string]any{"value": "0", "currency": currency, "issuer": issuerAddress}}
			},
		},
		{
			name: "PathSet",
			value: func(currency any) map[string]any {
				return map[string]any{"Paths": []any{[]any{map[string]any{"currency": currency}}}}
			},
		},
		{
			name: "nested XChainBridge",
			value: func(currency any) map[string]any {
				return map[string]any{
					"XChainBridge": map[string]any{
						"LockingChainDoor":  doorAddress,
						"LockingChainIssue": map[string]any{"currency": currency, "issuer": issuerAddress},
						"IssuingChainDoor":  doorAddress,
						"IssuingChainIssue": map[string]any{"currency": "XRP"},
					},
				}
			},
		},
	}
	invalid := []any{
		123,
		"1",
		"0xUSD",
		"0x0000000000000000000000005553440000000000",
	}
	for _, context := range contexts {
		for _, currency := range invalid {
			t.Run(fmt.Sprintf("%s/%v", context.name, currency), func(t *testing.T) {
				_, err := Encode(context.value(currency))
				require.Error(t, err)
				_, err = EncodeBytes(context.value(currency))
				require.Error(t, err)
			})
		}
	}
}

func TestEncodeDecodedFields_PreservesReservedCurrencies(t *testing.T) {
	fixtures := []struct {
		name         string
		wire         string
		strictReject bool
	}{
		{
			name:         "Currency noCurrency",
			wire:         "011A" + noCurrency,
			strictReject: true,
		},
		{
			name:         "Currency badCurrency",
			wire:         "011A" + badCurrency,
			strictReject: true,
		},
		{
			name:         "Issue noCurrency",
			wire:         "0318" + noCurrency + issuerBytes,
			strictReject: true,
		},
		{
			name:         "Issue badCurrency",
			wire:         "0318" + badCurrency + issuerBytes,
			strictReject: true,
		},
		{
			name:         "Amount zero noCurrency",
			wire:         "618000000000000000" + noCurrency + issuerBytes,
			strictReject: true,
		},
		{
			name:         "Amount zero badCurrency",
			wire:         "618000000000000000" + badCurrency + issuerBytes,
			strictReject: false,
		},
		{
			name:         "PathSet noCurrency",
			wire:         "011210" + noCurrency + "00",
			strictReject: true,
		},
		{
			name:         "PathSet badCurrency",
			wire:         "011210" + badCurrency + "00",
			strictReject: false,
		},
		{
			name:         "nested XChainBridge noCurrency",
			wire:         "011914" + doorBytes + noCurrency + issuerBytes + "14" + doorBytes + noCurrency + issuerBytes,
			strictReject: true,
		},
		{
			name:         "nested XChainBridge badCurrency",
			wire:         "011914" + doorBytes + badCurrency + issuerBytes + "14" + doorBytes + badCurrency + issuerBytes,
			strictReject: true,
		},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := Decode(tc.wire)
			require.NoError(t, err)

			strict, strictErr := Encode(decoded)
			if tc.strictReject {
				require.Error(t, strictErr)
			} else {
				require.NoError(t, strictErr)
				require.Equal(t, tc.wire, strict)
			}

			trusted, err := EncodeBytesTrusted(decoded)
			require.NoError(t, err)
			require.Equal(t, tc.wire, strings.ToUpper(hex.EncodeToString(trusted)))

			reencoded, err := EncodeDecodedFields(decoded)
			require.NoError(t, err)
			require.Equal(t, tc.wire, strings.ToUpper(hex.EncodeToString(reencoded)))
		})
	}
}

package binarycodec

import (
	"math"
	"strings"
	"testing"
)

// TestEncodeDecode_Int32Field round-trips an STObject carrying an INT32 field
// (STI_INT32 = 10) through the canonical binary form. sfLoanScale is the only
// 3.0.0 sfield of type Int32, so it doubles as the wire-format witness for the
// new type code.
func TestEncodeDecode_Int32Field(t *testing.T) {
	for _, scale := range []int{0, 1, -1, 12345, -12345, math.MaxInt32, math.MinInt32} {
		obj := map[string]any{"LoanScale": scale}

		enc, err := Encode(obj)
		if err != nil {
			t.Fatalf("Encode(LoanScale=%d): %v", scale, err)
		}

		dec, err := Decode(enc)
		if err != nil {
			t.Fatalf("Decode(%s): %v", enc, err)
		}

		got, ok := dec["LoanScale"]
		if !ok {
			t.Fatalf("LoanScale=%d: decoded object missing LoanScale (%v)", scale, dec)
		}
		if got != scale {
			t.Fatalf("LoanScale round-trip: encoded %d, decoded %v (%T)", scale, got, got)
		}
	}
}

// TestEncodeDecode_CounterpartySignature round-trips sfCounterpartySignature
// (sfield code 37, OBJECT) with its nested sfSigningPubKey, sfTxnSignature and
// sfSigners. It confirms the inner-object format resolves the nested signing
// fields rather than rejecting them, matching rippled's InnerObjectFormats
// registration.
func TestEncodeDecode_CounterpartySignature(t *testing.T) {
	const (
		pubKey = "0379F17CFA0FFD7518181594BE69FE9A10471D6DE1F4055C6D2746AFD6CF89889E"
		txnSig = "3045022100D55ED1953F860ADC1BC5CD993ABB927F48156ACA31C64737865F4F4FF6" +
			"D015A80220630704D2BD09C8E99F26090C25F11B28F5D96A1350454402C2CED92B39FFDBAF"
		account = "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p"
	)

	obj := map[string]any{
		"CounterpartySignature": map[string]any{
			"SigningPubKey": pubKey,
			"TxnSignature":  txnSig,
			"Signers": []any{
				map[string]any{
					"Signer": map[string]any{
						"Account":       account,
						"SigningPubKey": pubKey,
						"TxnSignature":  txnSig,
					},
				},
			},
		},
	}

	enc, err := Encode(obj)
	if err != nil {
		t.Fatalf("Encode(CounterpartySignature): %v", err)
	}

	dec, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode(%s): %v", enc, err)
	}

	inner, ok := dec["CounterpartySignature"].(map[string]any)
	if !ok {
		t.Fatalf("CounterpartySignature decoded as %T, want map (%v)", dec["CounterpartySignature"], dec)
	}
	for _, field := range []string{"SigningPubKey", "TxnSignature", "Signers"} {
		if _, ok := inner[field]; !ok {
			t.Errorf("decoded CounterpartySignature missing nested field %q (%v)", field, inner)
		}
	}

	// Re-encode the decoded object and require byte-for-byte equality: any field
	// the inner-object format failed to resolve would drop out or shift here.
	reEnc, err := Encode(dec)
	if err != nil {
		t.Fatalf("re-Encode(decoded): %v", err)
	}
	if !strings.EqualFold(enc, reEnc) {
		t.Fatalf("CounterpartySignature round-trip not idempotent:\n  first:  %s\n  second: %s", enc, reEnc)
	}
}

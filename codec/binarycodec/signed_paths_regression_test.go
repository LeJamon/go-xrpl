package binarycodec

import (
	"crypto/sha512"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// The signature is a placeholder; these vectors exercise signed-field serialization.
const (
	signedPathsAccountA = "rPDXxSZcuVL3ZWoyU82bcde3zwvmShkRyF"
	signedPathsAccountB = "rf1BiGeXwwQoi8Z2ueFYTEXSwuJYfV2Jpn"
	signedPathsIssuer   = "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp"
	signedPathsAccount  = "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys"
	signedPathsDest     = "rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH"
	signedPathsMPTID    = "0102030405060708090A0B0C0D0E0F101112131415161718"
	signedPathsPubKey   = "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3"

	signedPathsCommonFields       = "12000024000000016140000000000F424068400000000000000A"
	signedPathsSigningPubKeyField = "732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3"
	signedPathsSignatureField     = "7404DEADBEEF"
	signedPathsAccountField       = "8114DD76483FACDEE26E60D8A586BB58D09F27045C46"
	signedPathsDestinationField   = "831493B89AFCAD4C8EAC2B131C1331FEF12AE1522BBE"
	signedPathsPathsField         = "0112"
	signedPathsSignerSuffix       = "DD76483FACDEE26E60D8A586BB58D09F27045C46"

	signedPathsPathAWire   = "01F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F30000000000000000000000000555344000000000069D33B18D53385F8A3185516C2EDA5DEDB8AC5C6100000000000000000000000000000000000000000"
	signedPathsPathMPTWire = "614B4E9C06F24296074F7BC48F92A97916C6DC5EA90102030405060708090A0B0C0D0E0F10111213141516171869D33B18D53385F8A3185516C2EDA5DEDB8AC5C6400102030405060708090A0B0C0D0E0F101112131415161718"

	signedPathsOriginalPathSet  = signedPathsPathAWire + "FF" + signedPathsPathMPTWire + "FF" + signedPathsPathAWire + "00"
	signedPathsReorderedPathSet = signedPathsPathMPTWire + "FF" + signedPathsPathAWire + "FF" + signedPathsPathAWire + "00"

	signedPathsFullPrefix    = signedPathsCommonFields + signedPathsSigningPubKeyField + signedPathsSignatureField + signedPathsAccountField + signedPathsDestinationField + signedPathsPathsField
	signedPathsSigningPrefix = "53545800" + signedPathsCommonFields + signedPathsSigningPubKeyField + signedPathsAccountField + signedPathsDestinationField + signedPathsPathsField
	signedPathsMultiPrefix   = "534D5400" + signedPathsCommonFields + "7300" + signedPathsAccountField + signedPathsDestinationField + signedPathsPathsField

	signedPathsOriginalTransaction  = signedPathsFullPrefix + signedPathsOriginalPathSet
	signedPathsReorderedTransaction = signedPathsFullPrefix + signedPathsReorderedPathSet
	signedPathsOriginalSigning      = signedPathsSigningPrefix + signedPathsOriginalPathSet
	signedPathsReorderedSigning     = signedPathsSigningPrefix + signedPathsReorderedPathSet
	signedPathsOriginalMulti        = signedPathsMultiPrefix + signedPathsOriginalPathSet + signedPathsSignerSuffix
	signedPathsReorderedMulti       = signedPathsMultiPrefix + signedPathsReorderedPathSet + signedPathsSignerSuffix

	signedPathsOriginalHash         = "CE0B4AD3934903228059B78F77948584D76B70A0F2E8AE59CD026A43A60476D8"
	signedPathsReorderedHash        = "8EE5BC025F94032357332A0D1076D4B802A53A1FF30A0E238A7459157307BFA1"
	signedPathsOriginalSigningHash  = "EF6060A8C952B30F1AC51B3B34B76713A6838DA1ED25BA7266809CD8CB266688"
	signedPathsReorderedSigningHash = "89B490D421AF6D38DD5F6C477953A452FC6AF73FED136FC4B4599F65792E533D"
	signedPathsOriginalMultiHash    = "CF491B52FAAE43E85809BD017871C2C6E267AAF97505FAE3FF1B103E4CA1D173"
	signedPathsReorderedMultiHash   = "2168EA68A6F07DA106AC4AB0CEF1A62FAF8C7276A1C1B90554AD499B041F733A"
)

func signedPathsPathA() []any {
	return []any{
		map[string]any{"account": signedPathsAccountA},
		map[string]any{"currency": "USD", "issuer": signedPathsIssuer},
		map[string]any{"currency": "XRP"},
	}
}

func signedPathsPathMPT() []any {
	return []any{
		map[string]any{
			"account":         signedPathsAccountB,
			"mpt_issuance_id": signedPathsMPTID,
			"issuer":          signedPathsIssuer,
		},
		map[string]any{"mpt_issuance_id": signedPathsMPTID},
	}
}

func signedPathsOriginal() []any {
	return []any{signedPathsPathA(), signedPathsPathMPT(), signedPathsPathA()}
}

func signedPathsReordered() []any {
	return []any{signedPathsPathMPT(), signedPathsPathA(), signedPathsPathA()}
}

func signedPathsPayment(paths []any) map[string]any {
	return map[string]any{
		"TransactionType": "Payment",
		"Sequence":        uint32(1),
		"Amount":          "1000000",
		"Fee":             "10",
		"SigningPubKey":   signedPathsPubKey,
		"TxnSignature":    "DEADBEEF",
		"Account":         signedPathsAccount,
		"Destination":     signedPathsDest,
		"Paths":           paths,
	}
}

func signedPathsHash(payload string) string {
	b, err := hex.DecodeString(payload)
	if err != nil {
		panic(err)
	}
	h := sha512.Sum512(b)
	return strings.ToUpper(hex.EncodeToString(h[:32]))
}

func TestSignedPaymentPathsGolden(t *testing.T) {
	tests := []struct {
		name             string
		paths            func() []any
		encoded          string
		transactionHash  string
		signing          string
		signingHash      string
		multisigning     string
		multisigningHash string
		decodedPaths     []any
	}{
		{
			name:             "submitted order with duplicate path",
			paths:            signedPathsOriginal,
			encoded:          signedPathsOriginalTransaction,
			transactionHash:  signedPathsOriginalHash,
			signing:          signedPathsOriginalSigning,
			signingHash:      signedPathsOriginalSigningHash,
			multisigning:     signedPathsOriginalMulti,
			multisigningHash: signedPathsOriginalMultiHash,
			decodedPaths: []any{
				[]any{
					map[string]any{"account": signedPathsAccountA, "type": 1, "type_hex": "0000000000000001"},
					map[string]any{"currency": "USD", "issuer": signedPathsIssuer, "type": 48, "type_hex": "0000000000000030"},
					map[string]any{"currency": "XRP", "type": 16, "type_hex": "0000000000000010"},
				},
				[]any{
					map[string]any{"account": signedPathsAccountB, "mpt_issuance_id": signedPathsMPTID, "issuer": signedPathsIssuer, "type": 97, "type_hex": "0000000000000061"},
					map[string]any{"mpt_issuance_id": signedPathsMPTID, "type": 64, "type_hex": "0000000000000040"},
				},
				[]any{
					map[string]any{"account": signedPathsAccountA, "type": 1, "type_hex": "0000000000000001"},
					map[string]any{"currency": "USD", "issuer": signedPathsIssuer, "type": 48, "type_hex": "0000000000000030"},
					map[string]any{"currency": "XRP", "type": 16, "type_hex": "0000000000000010"},
				},
			},
		},
		{
			name:             "reordered paths with duplicate path",
			paths:            signedPathsReordered,
			encoded:          signedPathsReorderedTransaction,
			transactionHash:  signedPathsReorderedHash,
			signing:          signedPathsReorderedSigning,
			signingHash:      signedPathsReorderedSigningHash,
			multisigning:     signedPathsReorderedMulti,
			multisigningHash: signedPathsReorderedMultiHash,
			decodedPaths: []any{
				[]any{
					map[string]any{"account": signedPathsAccountB, "mpt_issuance_id": signedPathsMPTID, "issuer": signedPathsIssuer, "type": 97, "type_hex": "0000000000000061"},
					map[string]any{"mpt_issuance_id": signedPathsMPTID, "type": 64, "type_hex": "0000000000000040"},
				},
				[]any{
					map[string]any{"account": signedPathsAccountA, "type": 1, "type_hex": "0000000000000001"},
					map[string]any{"currency": "USD", "issuer": signedPathsIssuer, "type": 48, "type_hex": "0000000000000030"},
					map[string]any{"currency": "XRP", "type": 16, "type_hex": "0000000000000010"},
				},
				[]any{
					map[string]any{"account": signedPathsAccountA, "type": 1, "type_hex": "0000000000000001"},
					map[string]any{"currency": "USD", "issuer": signedPathsIssuer, "type": 48, "type_hex": "0000000000000030"},
					map[string]any{"currency": "XRP", "type": 16, "type_hex": "0000000000000010"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := signedPathsPayment(tt.paths())
			encoded, err := Encode(input)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if encoded != tt.encoded {
				t.Fatalf("Encode = %s, want %s", encoded, tt.encoded)
			}

			decoded, err := Decode(tt.encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got := decoded["Paths"]; !reflect.DeepEqual(got, tt.decodedPaths) {
				t.Fatalf("decoded Paths = %#v, want %#v", got, tt.decodedPaths)
			}
			reencoded, err := Encode(decoded)
			if err != nil {
				t.Fatalf("re-encode decoded transaction: %v", err)
			}
			if reencoded != tt.encoded {
				t.Fatalf("re-encoded transaction = %s, want %s", reencoded, tt.encoded)
			}

			if got := signedPathsHash("54584E00" + tt.encoded); got != tt.transactionHash {
				t.Fatalf("transaction hash = %s, want %s", got, tt.transactionHash)
			}
			signing, err := EncodeForSigning(input)
			if err != nil {
				t.Fatalf("EncodeForSigning: %v", err)
			}
			if signing != tt.signing {
				t.Fatalf("signing preimage = %s, want %s", signing, tt.signing)
			}
			if got := signedPathsHash(signing); got != tt.signingHash {
				t.Fatalf("signing hash = %s, want %s", got, tt.signingHash)
			}

			multisigning, err := EncodeForMultisigning(input, signedPathsAccount)
			if err != nil {
				t.Fatalf("EncodeForMultisigning: %v", err)
			}
			if multisigning != tt.multisigning {
				t.Fatalf("multisigning preimage = %s, want %s", multisigning, tt.multisigning)
			}
			if got := signedPathsHash(multisigning); got != tt.multisigningHash {
				t.Fatalf("multisigning hash = %s, want %s", got, tt.multisigningHash)
			}
		})
	}
}

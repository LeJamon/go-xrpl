package binarycodec

import (
	"crypto/sha512"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

const (
	roleAccount = "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys"
	roleKey     = "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3"
	roleSuffix  = "DD76483FACDEE26E60D8A586BB58D09F27045C46"

	// This is the independently derived canonical serialization of the
	// signing fields in roleTransaction. It intentionally excludes the
	// prefix and the multisigning account suffix.
	roleProjection      = "120003240000000168400000000000000A732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE38114" + roleSuffix
	roleMultiProjection = "120003240000000168400000000000000A73008114" + roleSuffix
)

func roleTransaction() map[string]any {
	return map[string]any{
		"TransactionType": "AccountSet",
		"Account":         roleAccount,
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   roleKey,
		"TxnSignature":    "DEADBEEF",
		"hash":            strings.Repeat("A", 64),
		"CounterpartySignature": map[string]any{
			"SigningPubKey": roleKey,
			"TxnSignature":  "DEADBEEF",
		},
		"SponsorSignature": map[string]any{
			"SigningPubKey": roleKey,
			"TxnSignature":  "DEADBEEF",
		},
	}
}

func sha512HalfHex(payload string) string {
	bytes, err := hex.DecodeString(payload)
	if err != nil {
		panic(err)
	}
	hash := sha512.Sum512(bytes)
	return strings.ToUpper(hex.EncodeToString(hash[:32]))
}

func TestEncodeForSigningRolePrefixesAndPreimages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		role           SigningRole
		cleanup        bool
		want           string
		wantSHA512Half string
	}{
		{
			name:           "transaction cleanup off",
			role:           TransactionRole,
			want:           "53545800" + roleProjection,
			wantSHA512Half: "265CDAAA79E6DCFCDF77546B2B5D983821672DA5A946747F11AA66995370ACBB",
		},
		{
			name:           "transaction cleanup on",
			role:           TransactionRole,
			cleanup:        true,
			want:           "53545800" + roleProjection,
			wantSHA512Half: "265CDAAA79E6DCFCDF77546B2B5D983821672DA5A946747F11AA66995370ACBB",
		},
		{
			name:           "counterparty cleanup off",
			role:           CounterpartyRole,
			want:           "53545800" + roleProjection,
			wantSHA512Half: "265CDAAA79E6DCFCDF77546B2B5D983821672DA5A946747F11AA66995370ACBB",
		},
		{
			name:           "counterparty cleanup on",
			role:           CounterpartyRole,
			cleanup:        true,
			want:           "43505400" + roleProjection,
			wantSHA512Half: "94F2D5EAA30DD72B3EB4E6AC8BEB6674EE0A835E6B5F8818BFB96C20549959EA",
		},
		{
			name:           "sponsor cleanup off",
			role:           SponsorRole,
			want:           "53545800" + roleProjection,
			wantSHA512Half: "265CDAAA79E6DCFCDF77546B2B5D983821672DA5A946747F11AA66995370ACBB",
		},
		{
			name:           "sponsor cleanup on",
			role:           SponsorRole,
			cleanup:        true,
			want:           "53504E00" + roleProjection,
			wantSHA512Half: "7C75233AF4407DD01ED467FFD450A515D4C646C1F6C46B5E11885AB4CC9CCA96",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := roleTransaction()
			before := roleTransaction()

			got, err := EncodeForSigningRole(input, test.role, test.cleanup)
			if err != nil {
				t.Fatalf("EncodeForSigningRole() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("EncodeForSigningRole() = %s, want %s", got, test.want)
			}
			if gotHash := sha512HalfHex(got); gotHash != test.wantSHA512Half {
				t.Fatalf("SHA-512 half = %s, want %s", gotHash, test.wantSHA512Half)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatalf("EncodeForSigningRole mutated its input: %#v", input)
			}
		})
	}
}

func TestEncodeForMultisigningRolePrefixesSuffixAndKeyProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		role           SigningRole
		cleanup        bool
		want           string
		wantSHA512Half string
	}{
		{
			name:           "transaction cleanup off blanks key",
			role:           TransactionRole,
			want:           "534D5400" + roleMultiProjection + roleSuffix,
			wantSHA512Half: "0F7E28C34CCC2700FA0D8A650DB06B8769C53EC61E5EED6C02FA7118A8598473",
		},
		{
			name:           "transaction cleanup on blanks key",
			role:           TransactionRole,
			cleanup:        true,
			want:           "534D5400" + roleMultiProjection + roleSuffix,
			wantSHA512Half: "0F7E28C34CCC2700FA0D8A650DB06B8769C53EC61E5EED6C02FA7118A8598473",
		},
		{
			name:           "counterparty cleanup off preserves key",
			role:           CounterpartyRole,
			want:           "534D5400" + roleProjection + roleSuffix,
			wantSHA512Half: "A2181B23C974ED67A2CF12B0A7F1B81B7D58D557451F0ADE67931E3CD2DED268",
		},
		{
			name:           "counterparty cleanup on preserves key",
			role:           CounterpartyRole,
			cleanup:        true,
			want:           "43504D00" + roleProjection + roleSuffix,
			wantSHA512Half: "15E8E173A158999DEE1D1FD0D7C5048FC61935EC5C10110FC39FF03C424AF8C6",
		},
		{
			name:           "sponsor cleanup off preserves key",
			role:           SponsorRole,
			want:           "534D5400" + roleProjection + roleSuffix,
			wantSHA512Half: "A2181B23C974ED67A2CF12B0A7F1B81B7D58D557451F0ADE67931E3CD2DED268",
		},
		{
			name:           "sponsor cleanup on preserves key",
			role:           SponsorRole,
			cleanup:        true,
			want:           "53504D00" + roleProjection + roleSuffix,
			wantSHA512Half: "49C2DE555D2D756CEDF5A23F8FA30C811F1F5F5D4F74054A3C000E681653A94F",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := roleTransaction()
			before := roleTransaction()

			got, err := EncodeForMultisigningRole(input, roleAccount, test.role, test.cleanup)
			if err != nil {
				t.Fatalf("EncodeForMultisigningRole() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("EncodeForMultisigningRole() = %s, want %s", got, test.want)
			}
			if gotHash := sha512HalfHex(got); gotHash != test.wantSHA512Half {
				t.Fatalf("SHA-512 half = %s, want %s", gotHash, test.wantSHA512Half)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatalf("EncodeForMultisigningRole mutated its input: %#v", input)
			}

			projection, err := Decode(got[8 : len(got)-len(roleSuffix)])
			if err != nil {
				t.Fatalf("Decode projection: %v", err)
			}
			wantKey := roleKey
			if test.role == TransactionRole {
				wantKey = ""
			}
			if projection["SigningPubKey"] != wantKey {
				t.Fatalf("projection SigningPubKey = %v, want %q", projection["SigningPubKey"], wantKey)
			}
			if _, ok := projection["TxnSignature"]; ok {
				t.Fatal("projection retained non-signing TxnSignature")
			}
		})
	}
}

func TestEncodeForMultisigningRoleRetainsLegacyTargetCompatibility(t *testing.T) {
	t.Parallel()

	input := roleTransaction()
	legacy, err := EncodeForMultisigningTarget(input, roleAccount)
	if err != nil {
		t.Fatalf("EncodeForMultisigningTarget() error = %v", err)
	}
	role, err := EncodeForMultisigningRole(input, roleAccount, CounterpartyRole, false)
	if err != nil {
		t.Fatalf("EncodeForMultisigningRole() error = %v", err)
	}
	if role != legacy {
		t.Fatalf("legacy target = %s, role target = %s", legacy, role)
	}
}

func TestEncodeForSigningRoleRejectsInvalidRoleAndUnknownField(t *testing.T) {
	t.Parallel()

	const invalidRole = SigningRole(99)
	if _, err := EncodeForSigningRole(nil, invalidRole, false); err == nil {
		t.Fatal("EncodeForSigningRole accepted an invalid role with cleanup disabled")
	}
	if _, err := EncodeForMultisigningRole(nil, roleAccount, invalidRole, false); err == nil {
		t.Fatal("EncodeForMultisigningRole accepted an invalid role with cleanup disabled")
	}

	input := roleTransaction()
	input["NotAField"] = true
	if _, err := EncodeForSigningRole(input, TransactionRole, false); err == nil {
		t.Fatal("EncodeForSigningRole accepted an unknown field")
	}
}

func TestEncodeForMultisigningRoleRejectsInvalidAccount(t *testing.T) {
	t.Parallel()

	if _, err := EncodeForMultisigningRole(roleTransaction(), "not-an-account", TransactionRole, false); err == nil {
		t.Fatal("EncodeForMultisigningRole accepted an invalid account")
	}
}

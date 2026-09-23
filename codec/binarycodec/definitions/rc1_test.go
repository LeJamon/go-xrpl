package definitions

import "testing"

func TestRC1FieldDefinitions(t *testing.T) {
	for _, tt := range []struct {
		name          string
		typeCode, nth int32
	}{
		{"LEVersion", 16, 6},
		{"ContractResult", 16, 21},
		{"VaultKind", 16, 22},
		{"SubscriptionDate", 2, 75},
		{"RedemptionDate", 2, 76},
	} {
		t.Run(tt.name, func(t *testing.T) {
			field, err := Get().FieldInstanceByName(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if field.Ordinal != tt.typeCode<<16|tt.nth || !field.IsSerialized || !field.IsSigningField || field.IsVLEncoded {
				t.Fatalf("incorrect field metadata: %+v", field)
			}
		})
	}
	for name := range Get().Fields() {
		if len(name) >= 4 && name[:4] == "Hook" {
			t.Errorf("obsolete field %s", name)
		}
	}
	for _, name := range []string{"EmitGeneration", "EmittedTxn"} {
		if Get().HasField(name) {
			t.Errorf("obsolete field %s", name)
		}
	}
	for _, name := range []string{"EmitBurden", "EmitDetails", "EmitParentTxnID", "EmitNonce"} {
		if !Get().HasField(name) {
			t.Errorf("missing retained field %s", name)
		}
	}
}

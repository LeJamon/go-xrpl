package definitions

import "testing"

func TestIsPseudoAccountField(t *testing.T) {
	pseudo := []string{"AMMID", "VaultID", "LoanBrokerID"}
	for _, name := range pseudo {
		if !IsPseudoAccountField(name) {
			t.Errorf("IsPseudoAccountField(%q) = false, want true", name)
		}
	}

	for _, name := range []string{"Account", "Balance", "Sequence", "Owner", "AMMID ", ""} {
		if IsPseudoAccountField(name) {
			t.Errorf("IsPseudoAccountField(%q) = true, want false", name)
		}
	}

	// The codec must recognize every pseudo-account designator: each is a Hash256
	// field present in definitions.json, otherwise the helper has drifted out of
	// sync with the wire format it describes.
	defs := Get()
	for _, name := range pseudo {
		fi, ok := defs.Fields[name]
		if !ok {
			t.Errorf("pseudo-account field %q missing from definitions", name)
			continue
		}
		if fi.Type != "Hash256" {
			t.Errorf("pseudo-account field %q has type %q, want Hash256", name, fi.Type)
		}
	}
}

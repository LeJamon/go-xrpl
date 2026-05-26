package config

import "testing"

func TestAmendmentsConfig_Validate(t *testing.T) {
	ok := AmendmentsConfig{Upvote: []string{"a", "b"}, Veto: []string{"c"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("disjoint lists should validate, got %v", err)
	}

	clash := AmendmentsConfig{Upvote: []string{"a", "b"}, Veto: []string{"b"}}
	if err := clash.Validate(); err == nil {
		t.Fatal("an amendment in both upvote and veto must be rejected")
	}

	empty := AmendmentsConfig{}
	if !empty.IsEmpty() {
		t.Fatal("zero-value AmendmentsConfig should be empty")
	}
	if empty.Validate() != nil {
		t.Fatal("empty config should validate")
	}
}

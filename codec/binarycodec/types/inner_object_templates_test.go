package types

import "testing"

func TestMeetsInnerObjectTemplate(t *testing.T) {
	valid := map[string]any{
		"Account":      "",
		"SignerWeight": 1,
	}
	if !MeetsInnerObjectTemplate("SignerEntry", valid) {
		t.Fatal("valid SignerEntry did not meet its template")
	}

	withDiscardable := map[string]any{
		"Account":      "",
		"SignerWeight": 1,
		"hash":         "",
	}
	if !MeetsInnerObjectTemplate("SignerEntry", withDiscardable) {
		t.Fatal("discardable field should be allowed by an inner object template")
	}

	withDisallowed := map[string]any{
		"Account":      "",
		"Amount":       "1",
		"SignerWeight": 1,
	}
	if MeetsInnerObjectTemplate("SignerEntry", withDisallowed) {
		t.Fatal("non-discardable field should not be allowed by an inner object template")
	}
}

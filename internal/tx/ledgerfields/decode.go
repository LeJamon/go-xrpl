package ledgerfields

// equalAmount compares two Amount values. XRP amounts decode to strings,
// IOU amounts to map[string]any{value, currency, issuer}, MPT amounts to
// map[string]any{value, mpt_issuance_id}. Returns true iff equivalent.
func equalAmount(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			vb, ok := bv[k]
			if !ok {
				return false
			}
			sa, aOK := va.(string)
			sb, bOK := vb.(string)
			if !aOK || !bOK || sa != sb {
				return false
			}
		}
		return true
	}
	return false
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

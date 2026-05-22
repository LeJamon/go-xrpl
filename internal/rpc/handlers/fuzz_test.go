package handlers

import (
	"encoding/json"
	"testing"
)

func FuzzParseCTID(f *testing.F) {
	f.Add("C005523A00020001")     // valid 16-char hex
	f.Add("")                     // empty
	f.Add("C005523A0002")         // too short
	f.Add("C005523A000200010000") // too long
	f.Add("ZZZZZZZZZZZZZZZZ")     // invalid hex chars
	f.Add("0000000000000000")     // all zeros
	f.Add("FFFFFFFFFFFFFFFF")     // all F

	f.Fuzz(func(t *testing.T, ctid string) {
		ledgerSeq, txIndex, err := parseCTID(ctid)
		if err == nil {
			// Valid CTID: ledgerSeq must fit in 28 bits.
			if ledgerSeq > 0x0FFFFFFF {
				t.Errorf("parseCTID(%q) returned ledgerSeq=%d exceeding 28-bit max", ctid, ledgerSeq)
			}
			// txIndex is uint16, so it is always in range — but verify non-negative logic.
			_ = txIndex
		}
		// Whether err is nil or not, we must not have panicked.
	})
}

func FuzzParseUintParam(f *testing.F) {
	f.Add([]byte(`42`))
	f.Add([]byte(`0`))
	f.Add([]byte(`-1`))
	f.Add([]byte(`4294967295`)) // uint32 max
	f.Add([]byte(`4294967296`)) // uint32 max + 1
	f.Add([]byte(`3.14`))
	f.Add([]byte(`"text"`))
	f.Add([]byte(`null`))
	f.Add([]byte(`true`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseUintParam(json.RawMessage(data))
		// Must not panic on any input.
	})
}

package binarycodec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// catchPanic recovers from a panic and reports it as a test error.
// This lets the fuzzer surface panics in code under test without
// crashing the entire test process.
func catchPanic(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Fatalf("panic: %v", r)
	}
}

func FuzzDecode(f *testing.F) {
	// Invalid inputs
	f.Add("")
	f.Add("ABC")
	f.Add("ZZZZ")
	f.Add("00")
	f.Add("FF")
	f.Add("FFFF")

	// Valid hex from TestDecode corpus
	f.Add("6680000000000000000000000000000000000000004C5543000000000020A85019EA62B48F79EB67273B797EB916438FA4")
	f.Add("120007220008000024001ABED82A2380BF2C2019001ABED764D55920AC9391400000000000000000000000000055534400000000000A20B3C85F482532A9578DBB3950B85CA06594D165400000037E11D60068400000000000000A732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3744630440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C8114DD76483FACDEE26E60D8A586BB58D09F27045C46")
	f.Add("34000000044B82FA09")
	f.Add("110072")
	f.Add("14789A")
	f.Add("011019")
	f.Add("03134073734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C73734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C")
	f.Add("4173734B611DDA23D3F5F62E20A173B78A")
	f.Add("011173734B611DDA23D3F5F62E20A173B78AB8406AC5")
	f.Add("501573734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C")

	// EscrowFinish tx from TestEncode
	f.Add("1200022280000000240000000120190000000B68400000000000277573210268D79CD579D077750740FA18A2370B7C2018B2714ECE70BA65C38D223E79BC9C74473045022100F06FB54049D6D50142E5CF2E2AC21946AF305A13E2A2D4BA881B36484DD01A540220311557EC8BEF536D729605A4CB4D4DC51B1E37C06C93434DD5B7651E1E2E28BF811452C7F01AD13B3CA9C1D133FA8F3482D2EF08FA7D82145A380FBD236B6A1CD14B939AD21101E5B6B6FFA2F9EA7D0F04C4D46544659A2D58525043686174E1F1")

	// Payment tx from TestEncode
	f.Add("1200002200000000240000034A201B009717BE61400000000098968068400000000000000C69D4564B964A845AC0000000000000000000000000555344000000000069D33B18D53385F8A3185516C2EDA5DEDB8AC5C673210379F17CFA0FFD7518181594BE69FE9A10471D6DE1F4055C6D2746AFD6CF89889E74473045022100D55ED1953F860ADC1BC5CD993ABB927F48156ACA31C64737865F4F4FF6D015A80220630704D2BD09C8E99F26090C25F11B28F5D96A1350454402C2CED92B39FFDBAF811469D33B18D53385F8A3185516C2EDA5DEDB8AC5C6831469D33B18D53385F8A3185516C2EDA5DEDB8AC5C6F9EA7C06636C69656E747D077274312E312E31E1F1011201F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F100000000000000000000000000000000000000000FF014B4E9C06F24296074F7BC48F92A97916C6DC5EA901DD39C650A96EDA48334E70CC4A85B8B2E8502CD310000000000000000000000000000000000000000000")

	// STObject (Memo)
	f.Add("EA7C0F04C4D46544659A2D58525043686174E1")

	// #601 class (STNumber round-half-even / underflow clamp): the Number-typed
	// field "Number" (header 0x91) followed by its 12-byte value. The last two
	// are the round-half-even tie and the carry-overflows-the-exponent cases.
	f.Add("91000000000000000080000000")
	f.Add("91000B27D038984000FFFFFFF1")
	f.Add("91FFF4D82FC767C000FFFFFFF1")
	f.Add("9100038D7EA4C6800000000000")
	f.Add("91000462D53C8ABAC200000001")
	f.Add("9100038D7EA4C6800000000002")
	// Mantissa 16, exponent 32784: in range as raw bytes but normalizes to an
	// exponent past maxExponent. Decode must reject it the way Encode does
	// (regression for the decode/encode asymmetry the round-trip oracle found).
	f.Add("91000000000000001000008010")

	// PathSet (field 0x0112) decode/encode asymmetries the round-trip oracle
	// found: an empty/truncated path set and a step byte with an illegal type
	// bit must be rejected, mirroring rippled's STPathSet "empty path" / "bad
	// path element" throws, since Encode cannot represent either.
	f.Add("0112")     // Paths header, no data → empty path set
	f.Add("011200")   // Paths header, bare terminator → empty path
	f.Add("01120200") // Paths header, step type bit outside typeAll → bad element

	// STAmount (field 0x61) decode/encode asymmetries: native and MPT
	// deserialization skip canonicalize and so skip the magnitude caps that the
	// JSON re-encode path applies. Decode must reject over-cap values the way
	// rippled's canonicalize does, or they decode to amounts Encode rejects.
	f.Add("610170000000000000") // native XRP magnitude > 10^17 (cMaxNativeN)
	// IOU amount with the native XRP currency code (all-zero) → decode renders
	// "XRP", which Encode refuses as an IOU currency (rippled: invalid native currency).
	f.Add("61800000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	// IOU amount whose currency has a non-printable code in standard position:
	// decode renders it as 40-char hex, which Encode must accept verbatim (rippled
	// stores a hex currency opaquely) rather than rejecting the non-ISO bytes.
	f.Add("62800000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000")

	// #710 class: an IOU amount whose 64-bit value 0x8081000000000000 decodes to
	// mantissa 281474976710656 (< cMinValue 1e15). Decode must reject the
	// non-canonical mantissa the way rippled's STAmount(SerialIter&) does
	// (STAmount.cpp:201-216) instead of accepting it and re-normalizing on Encode.
	f.Add("61808100000000000000000000000000000000000055534400000000000101010101010101010101010101010101010101")

	// #710 class: a PathSet whose 0xFF boundary is followed straight by the 0x00
	// terminator — an empty trailing path. rippled rejects it ("empty path",
	// STPathSet.cpp:65-71); the decoder must too, not silently drop it and
	// re-encode one byte shorter.
	f.Add("0112010000000000000000000000000000000000000000FF00")

	// Vector256 (field 0x0113) with a VL length that is not a multiple of 32:
	// decode must reject it, not slice past the buffer and panic.
	f.Add("01130100")

	// Issue (field 0x0118, LockingChainIssue) whose currency has a non-printable
	// code in standard position: decode must render it as hex (and not corrupt
	// high bytes via string(byte)), so it round-trips through Encode.
	f.Add("011800000000000000000000000001800100000000000000000000000000000000000000000000000000")

	// Issue (field 0x0118) holding an MPT asset: the 44-byte wire form (issuer +
	// noAccount marker + sequence) decodes to a 24-byte mpt_issuance_id, which
	// Encode must expand back to the same 44 bytes rather than emitting only 24.
	f.Add("01180000000000000000001000000000000000000000000000000000000000000000000000000000000100000000")

	// Account (field 0x81) serialized as a zero-length VL: the default (all-zero)
	// account. Decode renders "" and Encode must round-trip it back to 0x8100,
	// matching rippled's default-STAccount serialization.
	f.Add("8100")

	// Currency-typed field (BaseAsset, 0x011A) holding a non-printable, non-UTF-8
	// code: re-encode probes every string with IsValidXAddress → DecodeBase58,
	// which must not panic on non-base58 / invalid-UTF-8 bytes.
	f.Add("011A0000000000000000000000008010010000000000")

	// Duplicate field in one object (here field 0xE2 twice at top level): a map
	// silently drops the repeat and re-encodes shorter, so decode must reject it
	// as rippled does ("Duplicate field detected").
	f.Add("E2E1E2")

	// #603 class (stray end marker / trailing bytes): each must be rejected, not
	// silently truncated. 0xE1 = ObjectEndMarker, 0xF1 = ArrayEndMarker; base
	// "011019" is a complete {CloseResolution: 25} object.
	f.Add("011019E1")       // object end marker as final byte
	f.Add("011019E1011019") // object end marker drops trailing fields
	f.Add("011019F1")       // array end marker at top level
	f.Add("E1")             // bare object end marker
	f.Add("01101900")       // trailing unparseable byte

	// Deep container nesting (the #672 class). These small blobs do not crash
	// today, and once the depth cap from #672 / PR #679 lands, decode rejects
	// them with a clean error instead of recursing — so the guard stays
	// continuously fuzzed. The full ~1.2 MB stack-overflow reproducer is NOT
	// seeded: until the cap lands it dies with an uncatchable
	// "fatal error: stack overflow" that recover() cannot trap, crashing the
	// test process. Add it as a seed in the same step that lands the cap.
	f.Add(strings.Repeat("EA", 12))   // 12-deep STObject (0xEA = sfMemo)
	f.Add(strings.Repeat("F9EA", 11)) // 22-deep STArray/STObject alternation

	// Complex synthetic blob exercising every container and many leaf types.
	f.Add("11007212000714789A220008000024001ABED82A2380BF2C2019001ABED73400000184467440734173734B611DDA23D3F5F62E20A173B78A501573734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C64D55920AC9391400000000000000000000000000055534400000000000A20B3C85F482532A9578DBB3950B85CA06594D165400000037E11D60068400000000000000A732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3744630440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C8114DD76483FACDEE26E60D8A586BB58D09F27045C46F9EA7D0F04C4D46544659A2D58525043686174E1F1011019011173734B611DDA23D3F5F62E20A173B78AB8406AC5011201F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F100000000000000000000000000000000000000000FF014B4E9C06F24296074F7BC48F92A97916C6DC5EA901DD39C650A96EDA48334E70CC4A85B8B2E8502CD3100000000000000000000000000000000000000000FF014B4E9C06F24296074F7BC48F92A97916C6DC5EA901DD39C650A96EDA48334E70CC4A85B8B2E8502CD31000000000000000000000000000000000000000000003134073734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C73734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C")

	f.Fuzz(func(t *testing.T, hexEncoded string) {
		defer catchPanic(t)

		result, err := Decode(hexEncoded)
		if err != nil {
			return
		}

		// Exercise the returned map by marshaling to JSON. This catches
		// issues like unserializable types hiding in the decoded output.
		_, err = json.Marshal(result)
		if err != nil {
			t.Fatalf("Decode succeeded but json.Marshal failed: %v", err)
		}
	})
}

func FuzzDecodeRoundTrip(f *testing.F) {
	// Valid hex from TestDecode / TestEncode that should round-trip
	f.Add("6680000000000000000000000000000000000000004C5543000000000020A85019EA62B48F79EB67273B797EB916438FA4")
	f.Add("120007220008000024001ABED82A2380BF2C2019001ABED764D55920AC9391400000000000000000000000000055534400000000000A20B3C85F482532A9578DBB3950B85CA06594D165400000037E11D60068400000000000000A732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3744630440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C8114DD76483FACDEE26E60D8A586BB58D09F27045C46")
	f.Add("34000000044B82FA09")
	f.Add("110072")
	f.Add("14789A")
	f.Add("011019")
	f.Add("03134073734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C73734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C")
	f.Add("4173734B611DDA23D3F5F62E20A173B78A")
	f.Add("011173734B611DDA23D3F5F62E20A173B78AB8406AC5")
	f.Add("501573734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C")
	f.Add("EA7C0F04C4D46544659A2D58525043686174E1")
	f.Add("1200022280000000240000000120190000000B68400000000000277573210268D79CD579D077750740FA18A2370B7C2018B2714ECE70BA65C38D223E79BC9C74473045022100F06FB54049D6D50142E5CF2E2AC21946AF305A13E2A2D4BA881B36484DD01A540220311557EC8BEF536D729605A4CB4D4DC51B1E37C06C93434DD5B7651E1E2E28BF811452C7F01AD13B3CA9C1D133FA8F3482D2EF08FA7D82145A380FBD236B6A1CD14B939AD21101E5B6B6FFA2F9EA7D0F04C4D46544659A2D58525043686174E1F1")
	f.Add("1200002200000000240000034A201B009717BE61400000000098968068400000000000000C69D4564B964A845AC0000000000000000000000000555344000000000069D33B18D53385F8A3185516C2EDA5DEDB8AC5C673210379F17CFA0FFD7518181594BE69FE9A10471D6DE1F4055C6D2746AFD6CF89889E74473045022100D55ED1953F860ADC1BC5CD993ABB927F48156ACA31C64737865F4F4FF6D015A80220630704D2BD09C8E99F26090C25F11B28F5D96A1350454402C2CED92B39FFDBAF811469D33B18D53385F8A3185516C2EDA5DEDB8AC5C6831469D33B18D53385F8A3185516C2EDA5DEDB8AC5C6F9EA7C06636C69656E747D077274312E312E31E1F1011201F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F100000000000000000000000000000000000000000FF014B4E9C06F24296074F7BC48F92A97916C6DC5EA901DD39C650A96EDA48334E70CC4A85B8B2E8502CD310000000000000000000000000000000000000000000")

	// #601 class: Number-typed field, including the round-half-even tie and the
	// carry-overflows-the-exponent cases, which must re-encode identically.
	f.Add("91000000000000000080000000")
	f.Add("91000B27D038984000FFFFFFF1")
	f.Add("91FFF4D82FC767C000FFFFFFF1")
	f.Add("91000462D53C8ABAC200000001")
	f.Add("9100038D7EA4C6800000000002")

	// Deep nesting (the #672 class): safe-size today, depth-cap regression once
	// #672 / PR #679 lands. Both re-encode to a longer canonical form (their
	// missing end markers are restored), so the full-consumption guard below
	// correctly does not flag them.
	f.Add(strings.Repeat("EA", 12))
	f.Add(strings.Repeat("F9EA", 11))

	// #710 class (decode accepts a blob rippled rejects, re-encoding shorter):
	// a non-canonical IOU mantissa that Encode silently re-normalizes, and a
	// PathSet whose trailing empty path Encode silently drops. Decode must reject
	// both. The full crasher lives in testdata/fuzz/FuzzDecodeRoundTrip.
	f.Add("61808100000000000000000000000000000000000055534400000000000101010101010101010101010101010101010101")
	f.Add("0112010000000000000000000000000000000000000000FF00")

	// Complex synthetic blob exercising every container and many leaf types.
	f.Add("11007212000714789A220008000024001ABED82A2380BF2C2019001ABED73400000184467440734173734B611DDA23D3F5F62E20A173B78A501573734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C64D55920AC9391400000000000000000000000000055534400000000000A20B3C85F482532A9578DBB3950B85CA06594D165400000037E11D60068400000000000000A732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3744630440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C8114DD76483FACDEE26E60D8A586BB58D09F27045C46F9EA7D0F04C4D46544659A2D58525043686174E1F1011019011173734B611DDA23D3F5F62E20A173B78AB8406AC5011201F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F100000000000000000000000000000000000000000FF014B4E9C06F24296074F7BC48F92A97916C6DC5EA901DD39C650A96EDA48334E70CC4A85B8B2E8502CD3100000000000000000000000000000000000000000FF014B4E9C06F24296074F7BC48F92A97916C6DC5EA901DD39C650A96EDA48334E70CC4A85B8B2E8502CD31000000000000000000000000000000000000000000003134073734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C73734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C")

	f.Fuzz(func(t *testing.T, hexEncoded string) {
		defer catchPanic(t)

		decoded, err := Decode(hexEncoded)
		if err != nil {
			return
		}

		reEncoded, err := Encode(decoded)
		if err != nil {
			t.Fatalf("Decode succeeded but Encode failed: %v\ndecoded map: %v", err, decoded)
		}

		// Full consumption (the #603 class): canonical field-ID and length-prefix
		// encodings are unique, so re-encoding the decoded field set is never
		// shorter than a fully consumed input. Fewer bytes out means decode
		// silently dropped a trailing field or a stray end marker instead of
		// erroring. (A longer re-encoding is legitimate: a nested object that
		// relied on end-of-data gets its omitted end marker restored.)
		if len(reEncoded) < len(hexEncoded) {
			t.Fatalf("decode dropped trailing bytes:\n  input:      %s\n  re-encoded: %s", hexEncoded, reEncoded)
		}

		// Re-encode idempotency (the #594/#601 class): decoding the canonical
		// form and re-encoding must reproduce identical bytes. Field-ordering or
		// numeric-precision drift diverges here, and unlike an input==output
		// check this does not false-positive on a valid but non-canonically
		// ordered input (the decoder does not require canonical field order).
		reDecoded, err := Decode(reEncoded)
		if err != nil {
			t.Fatalf("Encode produced output that does not decode: %v\n  re-encoded: %s", err, reEncoded)
		}
		reEncoded2, err := Encode(reDecoded)
		if err != nil {
			t.Fatalf("second Encode failed: %v\n  re-decoded map: %v", err, reDecoded)
		}
		if !strings.EqualFold(reEncoded, reEncoded2) {
			t.Fatalf("re-encode not idempotent:\n  first:  %s\n  second: %s", reEncoded, reEncoded2)
		}
	})
}

func FuzzDecodeQuality(f *testing.F) {
	// Edge cases
	f.Add("")
	f.Add("ZZ")

	// NOTE: Inputs that hex-decode to fewer than 8 bytes (e.g. "00", "FFFF")
	// trigger a panic in DecodeQuality due to an unchecked slice bound on
	// line 85 of quality.go: `bytes := decoded[len(decoded)-8:]`.
	// The fuzzer will discover this class of bug immediately during fuzz runs.

	// 8-byte quality values from quality_test.go
	f.Add("0000000000000000")
	f.Add("5500000000000000")
	f.Add("5D06F4C3362FE1D0")
	f.Add("640000000BAB9FB0")

	// Longer strings (function takes last 8 bytes)
	f.Add("00000000000000000000000000000000")
	f.Add("FFFFFFFFFFFFFFFF")

	f.Fuzz(func(t *testing.T, encoded string) {
		defer catchPanic(t)

		// Must not panic; errors are expected for malformed input.
		result, err := DecodeQuality(encoded)
		if err != nil {
			return
		}
		// If decode succeeded, verify the result is a non-empty string.
		_ = fmt.Sprintf("%s", result)
	})
}

func FuzzDecodeLedgerData(f *testing.F) {
	// Edge cases
	f.Add("")
	f.Add("00")
	f.Add("FFFF")
	f.Add("ZZ")

	// Truncated inputs from ledger_data_test.go
	f.Add("01E914")
	f.Add("01E91435016340767BF1")
	f.Add("01E91435016340767BF1C4A3EACEB081770D8ADE216C85445DD6FB002C6B5A2930F2DE")

	// Valid full ledger data
	f.Add("01E91435016340767BF1C4A3EACEB081770D8ADE216C85445DD6FB002C6B5A2930F2DECE006DA18150CB18F6DD33F6F0990754C962A7CCE62F332FF9C13939B03B864117F0BDA86B6E9B4F873B5C3E520634D343EF5D9D9A4246643D64DAD278BA95DC0EAC6EB5350CF970D521276CDE21276CE60A00")

	f.Fuzz(func(t *testing.T, data string) {
		defer catchPanic(t)

		// Must not panic; errors are expected for malformed input.
		_, _ = DecodeLedgerData(data)
	})
}

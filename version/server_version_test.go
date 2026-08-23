package version

import (
	"encoding/binary"
	"testing"
)

func TestEncodedServerVersion(t *testing.T) {
	if got, want := EncodedServerVersion(), uint64(0x4000_0303_00C0_0000); got != want {
		t.Fatalf("EncodedServerVersion() = %#018x, want %#018x", got, want)
	}
	var wire [8]byte
	binary.BigEndian.PutUint64(wire[:], EncodedServerVersion())
	wantWire := [8]byte{0x40, 0x00, 0x03, 0x03, 0x00, 0xC0, 0x00, 0x00}
	if wire != wantWire {
		t.Fatalf("wire encoding = %x, want %x", wire, wantWire)
	}
}

func TestEncodeServerVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    uint64
	}{
		{name: "release", version: "1.2.3", want: 0x4000_0102_03C0_0000},
		{name: "beta", version: "1.2.3-b7", want: 0x4000_0102_0347_0000},
		{name: "release candidate", version: "1.2.4-rc7", want: 0x4000_0102_0487_0000},
		{name: "build metadata", version: "1.2.5+abcdef.DEBUG", want: 0x4000_0102_05C0_0000},
		{name: "later recognized prerelease", version: "1.2.6-alpha.rc7+abcdef", want: 0x4000_0102_0687_0000},
		{name: "unknown prerelease", version: "1.2.7-alpha", want: 0x4000_0102_0700_0000},
		{name: "missing beta ordinal", version: "1.2.8-b", want: 0x4000_0102_0800_0000},
		{name: "beta ordinal out of range", version: "1.2.8-b64", want: 0x4000_0102_0800_0000},
		{name: "rc ordinal out of range", version: "1.2.8-rc64", want: 0x4000_0102_0800_0000},
		{name: "zero prerelease", version: "1.2.9-b0", want: 0x4000_0102_0940_0000},
		{name: "maximum", version: "255.255.255-b63", want: 0x4000_FFFF_FF7F_0000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := encodeServerVersion(test.version)
			if err != nil {
				t.Fatalf("encodeServerVersion(%q): %v", test.version, err)
			}
			if got != test.want {
				t.Errorf("encodeServerVersion(%q) = %#018x, want %#018x", test.version, got, test.want)
			}
		})
	}
}

func TestEncodeServerVersionRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{
		"",
		"v1.2.3",
		"1.2",
		"1.2.3.4",
		"01.2.3",
		"256.2.3",
		"1.256.3",
		"1.2.256",
		"1.2.3-0",
		"1.2.3-01",
		"1.2.3-01alpha",
		"1.2.3-0abc",
		"1.2.3-alpha.01",
		"1.2.3+",
		"1.2.3+bad_metadata",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := encodeServerVersion(value); err == nil {
				t.Fatalf("encodeServerVersion(%q) succeeded, want error", value)
			}
		})
	}
}

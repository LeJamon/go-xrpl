package entry

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

func FuzzAppendHexDecoded(f *testing.F) {
	for _, s := range []string{"", "0", "00", "aB", "abc", "0G", "G0", "zz9", "DEADBEEF"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, wantErr := hex.DecodeString(s)
		got, gotErr := appendHexDecoded([]byte{0x7F}, s)
		if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
			t.Fatalf("%q: error %v, hex.DecodeString %v", s, gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, append([]byte{0x7F}, want...)) {
			t.Fatalf("%q: decoded %X, want %X", s, got[1:], want)
		}
	})
}

func FuzzCanonicalNativeAmount(f *testing.F) {
	for _, s := range []string{"0", "1", "-1", "-0", "+1", "01", "100000000000000000", "100000000000000001", "-100000000000000000", "1.0", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		wire, ok := canonicalNativeAmount(s)
		if !ok {
			return
		}
		want, err := (&types.Amount{}).FromJSON(s)
		if err != nil {
			t.Fatalf("%q: fast path accepted what the codec rejects: %v", s, err)
		}
		if got := binary.BigEndian.AppendUint64(nil, wire); !bytes.Equal(got, want) {
			t.Fatalf("%q: wire %X, codec %X", s, got, want)
		}
	})
}

func TestAppendBlobMatchesCodec(t *testing.T) {
	for _, s := range []string{"", "0", "A", "ABC", "abcd", "ZZ", "0G", "G", "AG", "GA0"} {
		want, wantErr := (&types.Blob{}).FromJSON(s)
		got, gotErr := appendBlob(nil, s)
		if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
			t.Fatalf("%q: error %v, codec %v", s, gotErr, wantErr)
		}
		if gotErr == nil && !bytes.Equal(got, append(appendVariableLength(nil, len(want)), want...)) {
			t.Fatalf("%q: got %X, codec %X", s, got, want)
		}
	}
	long := make([]byte, 2*maxVLLength+1)
	for i := range long {
		long[i] = 'A'
	}
	if _, err := appendBlob(nil, string(long)); !errors.Is(err, serdes.ErrLengthPrefixTooLong) {
		t.Fatalf("oversized blob error = %v", err)
	}
	long[len(long)-1] = 'G'
	if _, err := appendBlob(nil, string(long)); !errors.As(err, new(hex.InvalidByteError)) {
		t.Fatalf("oversized invalid blob error = %v, want the hex error first", err)
	}
}

func TestSizeHintsSkipOversizedFields(t *testing.T) {
	if n := blobEncodedLen(strings.Repeat("0", 2*maxVLLength+1)); n != 0 {
		t.Fatalf("oversized blob size hint = %d", n)
	}
	if n := vector256EncodedLen(make([]string, types.MaxJSONArrayElements+1)); n != 0 {
		t.Fatalf("oversized vector size hint = %d", n)
	}
	page := nftCodecPage(t, 1, maxVLLength+1)
	if n := nftokensEncodedLen(page.NFTokens); n != 0 {
		t.Fatalf("oversized URI size hint = %d", n)
	}
	if _, err := page.Encode(); !errors.Is(err, serdes.ErrLengthPrefixTooLong) {
		t.Fatalf("oversized URI error = %v", err)
	}
}

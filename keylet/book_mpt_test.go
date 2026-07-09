package keylet

import (
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
)

// TestBookBaseIssueMatchesBookDir asserts BookBase reproduces the existing
// Issue/Issue book key byte-for-byte, so introducing MPT-denominated books
// cannot shift the keys of any existing (non-MPT) order book.
func TestBookBaseIssueMatchesBookDir(t *testing.T) {
	paysCur := [20]byte{12: 'U', 13: 'S', 14: 'D'}
	paysIss := [20]byte{1, 2, 3}
	getsCur := [20]byte{12: 'E', 13: 'U', 14: 'R'}
	getsIss := [20]byte{4, 5, 6}

	want := BookDir(paysCur, paysIss, getsCur, getsIss)
	got := BookBase(IssueSide(paysCur, paysIss), IssueSide(getsCur, getsIss), nil)
	if got.Key != want.Key {
		t.Fatalf("BookBase Issue/Issue key mismatch:\n want %x\n got  %x", want.Key, got.Key)
	}

	domain := [32]byte{9, 9, 9}
	wantD := BookDirWithDomain(paysCur, paysIss, getsCur, getsIss, domain)
	gotD := BookBase(IssueSide(paysCur, paysIss), IssueSide(getsCur, getsIss), &domain)
	if gotD.Key != wantD.Key {
		t.Fatalf("BookBase Issue/Issue+domain key mismatch:\n want %x\n got  %x", wantD.Key, gotD.Key)
	}
}

// TestBookBaseMPTFieldLayout pins the hashed-field layout for each Issue/MPT
// combination to rippled getBookBase (Indexes.cpp): the two asset fields first
// (currency for an Issue side, the 192-bit id for an MPT side), then the issuer
// of any Issue side, then the optional domain. It reconstructs the expected key
// directly from indexHash so a future field-order regression is caught.
func TestBookBaseMPTFieldLayout(t *testing.T) {
	paysCur := [20]byte{12: 'U', 13: 'S', 14: 'D'}
	paysIss := [20]byte{1, 2, 3}
	getsCur := [20]byte{12: 'E', 13: 'U', 14: 'R'}
	getsIss := [20]byte{4, 5, 6}
	paysMPT := MakeMPTID(7, [20]byte{7, 7, 7})
	getsMPT := MakeMPTID(8, [20]byte{8, 8, 8})

	expect := func(fields ...[]byte) [32]byte {
		return indexHash(spaceBookDir, fields...)
	}

	cases := []struct {
		name string
		pays BookSide
		gets BookSide
		want [32]byte
	}{
		{
			name: "Issue pays / MPT gets",
			pays: IssueSide(paysCur, paysIss),
			gets: MPTSide(getsMPT),
			want: expect(paysCur[:], getsMPT[:], paysIss[:]),
		},
		{
			name: "MPT pays / Issue gets",
			pays: MPTSide(paysMPT),
			gets: IssueSide(getsCur, getsIss),
			want: expect(paysMPT[:], getsCur[:], getsIss[:]),
		},
		{
			name: "MPT pays / MPT gets",
			pays: MPTSide(paysMPT),
			gets: MPTSide(getsMPT),
			want: expect(paysMPT[:], getsMPT[:]),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BookBase(tc.pays, tc.gets, nil)
			if got.Key != tc.want {
				t.Fatalf("key mismatch:\n want %x\n got  %x", tc.want, got.Key)
			}
		})
	}
}

// TestBookBaseMPTDomainAppends confirms the domain id is appended last, after
// the MPT/issuer fields.
func TestBookBaseMPTDomainAppends(t *testing.T) {
	paysMPT := MakeMPTID(7, [20]byte{7, 7, 7})
	getsMPT := MakeMPTID(8, [20]byte{8, 8, 8})
	domain := [32]byte{0xAB, 0xCD}

	got := BookBase(MPTSide(paysMPT), MPTSide(getsMPT), &domain)
	var spaceBytes [2]byte
	spaceBytes[0] = byte(spaceBookDir >> 8)
	spaceBytes[1] = byte(spaceBookDir)
	want := sha512half.Sum(spaceBytes[:], paysMPT[:], getsMPT[:], domain[:])
	if got.Key != want {
		t.Fatalf("key mismatch:\n want %x\n got  %x", want, got.Key)
	}
}

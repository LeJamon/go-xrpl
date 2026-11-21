package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestValidSkipListProof(t *testing.T) {
	closed := makeClosedLedgerWithTxs(t, nil)
	lookup := newFakeLookup()
	lookup.add(closed)
	hash := closed.Hash()
	key := keylet.LedgerHashes().Key
	wireHeader, path, err := newLedgerProviderForTest(lookup).GetProofPath(hash[:], key[:], message.LedgerMapAccountState)
	require.NoError(t, err)
	good := message.ProofPathResponse{
		Key: key[:], LedgerHash: hash[:], MapType: message.LedgerMapAccountState,
		LedgerHeader: wireHeader, Path: path,
	}
	for _, tc := range []struct {
		name   string
		change func(*message.ProofPathResponse)
		valid  bool
	}{
		{"verified", func(*message.ProofPathResponse) {}, true},
		{"header with trailing bytes", func(p *message.ProofPathResponse) {
			p.LedgerHeader = append(append([]byte(nil), p.LedgerHeader...), 0)
		}, true},
		{"transaction map", func(p *message.ProofPathResponse) { p.MapType = message.LedgerMapTransaction }, false},
		{"other state key", func(p *message.ProofPathResponse) { p.Key = make([]byte, 32) }, false},
		{"short header", func(p *message.ProofPathResponse) { p.LedgerHeader = wireHeader[:len(wireHeader)-1] }, false},
		{"header hash mismatch", func(p *message.ProofPathResponse) { p.LedgerHash = make([]byte, 32) }, false},
		{"empty path", func(p *message.ProofPathResponse) { p.Path = nil }, false},
		{"incomplete path", func(p *message.ProofPathResponse) { p.Path = path[:len(path)-1] }, false},
		{"wrong state root", func(p *message.ProofPathResponse) {
			h := closed.Header()
			h.AccountHash[0] ^= 1
			hash := header.CalculateHash(h)
			p.LedgerHeader, p.LedgerHash = header.AddRaw(h, false), hash[:]
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := good
			tc.change(&resp)
			require.Equal(t, tc.valid, validSkipListProof(&resp))
		})
	}
	for _, length := range []int{0, 31, 33} {
		for _, field := range []string{"key", "ledger hash"} {
			resp := good
			if field == "key" {
				resp.Key = make([]byte, length)
			} else {
				resp.LedgerHash = make([]byte, length)
			}
			require.False(t, validSkipListProof(&resp), "%s length %d", field, length)
		}
	}
}

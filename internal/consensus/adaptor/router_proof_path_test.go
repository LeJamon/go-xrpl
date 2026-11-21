package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestProofPathResponseFee(t *testing.T) {
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
		fee    resource.Charge
	}{
		{"verified", func(*message.ProofPathResponse) {}, resource.FeeTrivialPeer()},
		{"header with trailing bytes", func(p *message.ProofPathResponse) {
			p.LedgerHeader = append(append([]byte(nil), p.LedgerHeader...), 0)
		}, resource.FeeTrivialPeer()},
		{"peer error precedes proof checks", func(p *message.ProofPathResponse) {
			p.Error, p.LedgerHeader, p.Path = message.ReplyErrorNoNode, nil, nil
		}, resource.FeeInvalidData()},
		{"transaction map", func(p *message.ProofPathResponse) { p.MapType = message.LedgerMapTransaction }, resource.FeeMalformedData()},
		{"other state key", func(p *message.ProofPathResponse) { p.Key = make([]byte, 32) }, resource.FeeMalformedData()},
		{"short header", func(p *message.ProofPathResponse) { p.LedgerHeader = wireHeader[:len(wireHeader)-1] }, resource.FeeMalformedData()},
		{"header hash mismatch", func(p *message.ProofPathResponse) { p.LedgerHash = make([]byte, 32) }, resource.FeeMalformedData()},
		{"empty path", func(p *message.ProofPathResponse) { p.Path = nil }, resource.FeeMalformedData()},
		{"incomplete path", func(p *message.ProofPathResponse) { p.Path = path[:len(path)-1] }, resource.FeeMalformedData()},
		{"wrong state root", func(p *message.ProofPathResponse) {
			h := closed.Header()
			h.AccountHash[0] ^= 1
			hash := header.CalculateHash(h)
			p.LedgerHeader, p.LedgerHash = header.AddRaw(h, false), hash[:]
		}, resource.FeeMalformedData()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := good
			tc.change(&resp)
			payload, err := message.Encode(&resp)
			require.NoError(t, err)
			require.Equal(t, tc.fee, proofPathResponseFee(payload))
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
			payload, err := message.Encode(&resp)
			require.NoError(t, err)
			require.Equal(t, resource.FeeMalformedData(), proofPathResponseFee(payload), "%s length %d", field, length)
		}
	}
	require.Equal(t, resource.FeeMalformedData(), proofPathResponseFee(nil))
}

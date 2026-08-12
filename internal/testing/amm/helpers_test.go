package amm

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type stubAMMReader struct {
	exists    bool
	existsErr error
	data      []byte
	readErr   error
}

func (r stubAMMReader) Exists(keylet.Keylet) (bool, error) { return r.exists, r.existsErr }
func (r stubAMMReader) Read(keylet.Keylet) ([]byte, error) { return r.data, r.readErr }

func TestLookupAMMDataFailsClosed(t *testing.T) {
	xrp := tx.Asset{Currency: "XRP"}
	usd := tx.Asset{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}

	data, found, err := lookupAMMData(stubAMMReader{}, xrp, usd)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, data)

	for name, reader := range map[string]stubAMMReader{
		"exists error": {existsErr: errors.New("exists failed")},
		"read error":   {exists: true, readErr: errors.New("read failed")},
		"missing data": {exists: true},
		"malformed":    {exists: true, data: []byte{0xff}},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := lookupAMMData(reader, xrp, usd)
			require.Error(t, err)
		})
	}

	_, _, err = lookupAMMData(stubAMMReader{}, xrp, tx.Asset{Currency: "USD", Issuer: "invalid"})
	require.Error(t, err)
	_, _, err = lookupAMMData(stubAMMReader{}, xrp, tx.Asset{MPTIssuanceID: "invalid"})
	require.Error(t, err)

	for name, asset := range map[string]tx.Asset{
		"XRP with issuer":       {Currency: "XRP", Issuer: usd.Issuer},
		"malformed currency":    {Currency: "US D", Issuer: usd.Issuer},
		"zero currency":         {Currency: "0000000000000000000000000000000000000000", Issuer: usd.Issuer},
		"zero issuer":           {Currency: "USD", Issuer: "rrrrrrrrrrrrrrrrrrrrrhoLvTp"},
		"MPT with zero issuer":  {MPTIssuanceID: "000000000000000000000000000000000000000000000000"},
		"MPT with issue fields": {Currency: "USD", Issuer: usd.Issuer, MPTIssuanceID: "00000001BAA9375DB1D7557B5E9E47D91098911DAF5CA558"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := lookupAMMData(stubAMMReader{}, xrp, asset)
			require.Error(t, err)
		})
	}
}

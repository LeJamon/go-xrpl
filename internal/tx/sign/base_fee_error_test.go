package sign

import (
	"errors"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type panicBaseFeeTx struct {
	*txcore.BaseTx
}

func (panicBaseFeeTx) CalculateBaseFee(txcore.LedgerView, txcore.EngineConfig) uint64 {
	panic("controlled base-fee failure")
}

func TestCalculateBaseFeePanicReturnsTypedTefException(t *testing.T) {
	txn := &panicBaseFeeTx{BaseTx: txcore.NewBaseTx(txcore.TypeAccountSet, "account")}

	fee, err := CalculateBaseFee(txn, nil, txcore.EngineConfig{BaseFee: 10})

	if fee != 0 {
		t.Fatalf("fee = %d, want 0 on calculator panic", fee)
	}
	resultErr, ok := ter.AsResultError(err)
	if !ok {
		t.Fatalf("error = %T (%v), want *ter.ResultError", err, err)
	}
	if resultErr.Code != ter.TefEXCEPTION {
		t.Fatalf("error code = %s, want tefEXCEPTION", resultErr.Code)
	}
}

var _ txcore.CustomBaseFeeCalculator = (*panicBaseFeeTx)(nil)

type baseFeeReadView struct {
	txcore.LedgerView
	data  []byte
	err   error
	reads int
}

func (v *baseFeeReadView) Read(keylet.Keylet) ([]byte, error) {
	v.reads++
	return v.data, v.err
}

func TestRegularKeyBaseFeeAccountReadFailure(t *testing.T) {
	const masterKey = "ED0000000000000000000000000000000000000000000000000000000000000001"
	const otherKey = "ED0000000000000000000000000000000000000000000000000000000000000002"
	account, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(masterKey)
	require.NoError(t, err)
	unspent, err := state.SerializeAccountRoot(&state.AccountRoot{Account: account})
	require.NoError(t, err)
	spent, err := state.SerializeAccountRoot(&state.AccountRoot{Account: account, Flags: state.LsfPasswordSpent})
	require.NoError(t, err)
	readErr := errors.New("account storage unavailable")
	for _, test := range []struct {
		name    string
		key     string
		data    []byte
		err     error
		skipSig bool
		inner   bool
		reads   int
		wantFee uint64
		wantErr bool
	}{
		{name: "master storage failure", key: masterKey, err: readErr, reads: 1, wantErr: true},
		{name: "master decode failure", key: masterKey, data: []byte{0xFF}, reads: 1, wantErr: true},
		{name: "absent account", key: masterKey, reads: 1, wantFee: 10},
		{name: "unspent waiver", key: masterKey, data: unspent, reads: 1},
		{name: "spent waiver", key: masterKey, data: spent, reads: 1, wantFee: 10},
		{name: "non-master skips read", key: otherKey, err: readErr, wantFee: 10},
		{name: "invalid key skips read", key: "invalid", err: readErr, wantFee: 10},
		{name: "unsigned skips read", err: readErr, wantFee: 10},
		{name: "standalone read failure", skipSig: true, err: readErr, reads: 1, wantErr: true},
		{name: "inner skips read", skipSig: true, inner: true, err: readErr, wantFee: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			txn := txcore.NewBaseTx(txcore.TypeRegularKeySet, account)
			txn.SigningPubKey = test.key
			if test.inner {
				txn.SetFlags(txcore.TfInnerBatchTxn)
			}
			view := &baseFeeReadView{data: test.data, err: test.err}
			fee, err := CalculateBaseFee(txn, view, txcore.EngineConfig{BaseFee: 10, SkipSignatureVerification: test.skipSig})
			if test.wantErr {
				var result *ter.ResultError
				require.ErrorAs(t, err, &result)
				require.Equal(t, ter.TefEXCEPTION, result.Code)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.wantFee, fee)
			require.Equal(t, test.reads, view.reads)
		})
	}
}

package delegate_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accountset "github.com/LeJamon/go-xrpl/internal/testing/accountset"
	mpttester "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	trustset "github.com/LeJamon/go-xrpl/internal/testing/trustset"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestDelegate_TrustlineAuthorizeGranularEffect(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(gw, alice, bob)
	env.EnableRequireAuth(gw)
	env.Close()

	env.Trust(alice, gw.IOU("USD", 1000))
	env.Close()
	require.Equal(t, "tesSUCCESS", grantPermissions(env, gw, bob, "TrustlineAuthorize").Code)
	env.Close()

	trust := trustset.TrustLine(gw, "USD", alice, "0").SetAuth().BuildTrustSet()
	trust.Delegate = bob.Address
	jtx.RequireTxSuccess(t, env.SubmitSignedWith(trust, bob))
	env.Close()

	data, err := env.LedgerEntry(keylet.Line(gw.ID, alice.ID, "USD"))
	require.NoError(t, err)
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	require.NotZero(t, line.Flags&(state.LsfLowAuth|state.LsfHighAuth))
}

func TestDelegate_AccountDomainGranularEffect(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "AccountDomainSet").Code)
	env.Close()

	set := accountset.AccountSet(alice).Domain("6578616D706C652E636F6D").BuildAccountSet()
	set.Delegate = bob.Address
	jtx.RequireTxSuccess(t, env.SubmitSignedWith(set, bob))
	env.Close()
	require.Equal(t, "example.com", env.AccountInfo(alice).Domain)
}

func TestDelegate_MPTokenIssuanceLockGranularEffect(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	issuer := jtx.NewAccount("issuer")
	bob := jtx.NewAccount("bob")
	m := mpttester.NewMPTTester(t, env, issuer, mpttester.MPTInit{Holders: []*jtx.Account{bob}})
	env.Close()
	m.Create(mpttester.CreateOpts{Flags: mpttx.MPTokenIssuanceCreateFlagCanLock})
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, issuer, bob, "MPTokenIssuanceLock").Code)
	env.Close()

	set := mpttx.NewMPTokenIssuanceSet(issuer.Address, m.IssuanceID())
	set.SetFlags(mpttx.MPTokenIssuanceSetFlagLock)
	set.Delegate = bob.Address
	jtx.RequireTxSuccess(t, env.SubmitSignedWith(set, bob))
	env.Close()
	m.CheckIssuanceFlags(entry.LsfMPTCanLock | entry.LsfMPTLocked)
}

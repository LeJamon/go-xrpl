package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/stretchr/testify/require"
)

func dynamicMPTDisabledEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.DisableFeature("DynamicMPT")
	env.Close()
	return env
}

func dynamicMPTEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("DynamicMPT")
	env.Close()
	return env
}

func confidentialDynamicMPTEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("DynamicMPT")
	env.EnableFeature("ConfidentialTransfer")
	env.Close()
	return env
}

func TestMPTImmutableFlagsCreate(t *testing.T) {
	t.Run("requires DynamicMPT before value validation", func(t *testing.T) {
		env := dynamicMPTDisabledEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		for _, flags := range []uint32{mpt.TifMPTCanLock, 0} {
			tester.Create(mpt.CreateOpts{ImmutableFlags: mpt.PtrUint32(flags), Err: jtx.TemDISABLED})
		}
	})

	t.Run("rejects zero and unknown bits", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		for _, flags := range []uint32{0, 1, 17, 65535} {
			tester.Create(mpt.CreateOpts{ImmutableFlags: mpt.PtrUint32(flags), Err: jtx.TemINVALID_FLAG})
		}
	})

	t.Run("persists every immutable bit", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		flags := mpt.TifMPTCanLock | mpt.TifMPTRequireAuth | mpt.TifMPTCanEscrow |
			mpt.TifMPTCanTrade | mpt.TifMPTCanTransfer | mpt.TifMPTCanClawback |
			mpt.TifMPTCanHoldConfidentialBalance | mpt.TifMPTMetadata | mpt.TifMPTTransferFee
		tester.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), ImmutableFlags: mpt.PtrUint32(flags)})
		env.Close()
		tester.CheckImmutableFlags(flags)
	})

	t.Run("confidential capability requires its amendment", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{
			ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanHoldConfidentialBalance),
			Err:            jtx.TemDISABLED,
		})
		tester.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanHoldConfidentialBalance, Err: jtx.TemDISABLED})
	})

	t.Run("confidential balances reject a positive transfer fee", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{
			Flags:       mpt.TfMPTCanTransfer | mpt.TfMPTCanHoldConfidentialBalance,
			TransferFee: mpt.PtrUint16(1),
			Err:         jtx.TemBAD_TRANSFER_FEE,
		})
	})
}

func TestMPTImmutableFlagsSetPreflight(t *testing.T) {
	t.Run("all mutation forms require DynamicMPT", func(t *testing.T) {
		env := dynamicMPTDisabledEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)
		tester.Set(mpt.SetOpts{Account: alice, ID: id, Flags: mpt.TfMPTSetCanLock, Err: jtx.TemDISABLED})
		tester.Set(mpt.SetOpts{Account: alice, ID: id, Metadata: mpt.PtrString(""), Err: jtx.TemDISABLED})
		tester.Set(mpt.SetOpts{Account: alice, ID: id, TransferFee: mpt.PtrUint16(0), Err: jtx.TemDISABLED})
		tester.Set(mpt.SetOpts{Account: alice, ID: id, ImmutableFlags: mpt.PtrUint32(0), Err: jtx.TemDISABLED})
	})

	t.Run("mutation cannot target a holder", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		tester := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)
		tester.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, Flags: mpt.TfMPTSetCanLock, Err: jtx.TemMALFORMED})
		tester.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, Metadata: mpt.PtrString("00"), Err: jtx.TemMALFORMED})
		tester.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, TransferFee: mpt.PtrUint16(0), Err: jtx.TemMALFORMED})
		tester.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanLock), Err: jtx.TemMALFORMED})
	})

	t.Run("mutation cannot accompany lock or unlock", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanLock})
		env.Close()
		for _, flags := range []uint32{mpt.TfMPTLock, mpt.TfMPTUnlock} {
			tester.Set(mpt.SetOpts{Account: alice, Flags: flags | mpt.TfMPTSetCanTrade, Err: jtx.TemMALFORMED})
			tester.Set(mpt.SetOpts{Account: alice, Flags: flags, Metadata: mpt.PtrString("00"), Err: jtx.TemMALFORMED})
			tester.Set(mpt.SetOpts{Account: alice, Flags: flags, TransferFee: mpt.PtrUint16(0), Err: jtx.TemMALFORMED})
			tester.Set(mpt.SetOpts{Account: alice, Flags: flags, ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanTrade), Err: jtx.TemMALFORMED})
		}
	})

	t.Run("validates immutable values and field boundaries", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{})
		env.Close()
		for _, flags := range []uint32{0, 1, 0x40000} {
			tester.Set(mpt.SetOpts{Account: alice, ImmutableFlags: mpt.PtrUint32(flags), Err: jtx.TemINVALID_FLAG})
		}
		tester.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(50001), Err: jtx.TemBAD_TRANSFER_FEE})
		tester.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString(hexRepeat("61", 1025)), Err: jtx.TemMALFORMED})
	})

	t.Run("confidential mutations require ConfidentialTransfer", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{})
		env.Close()
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanHoldConfidentialBalance, Err: jtx.TemDISABLED})
		tester.Set(mpt.SetOpts{Account: alice, ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanHoldConfidentialBalance), Err: jtx.TemDISABLED})
	})
}

func TestMPTMutableByDefault(t *testing.T) {
	env := dynamicMPTEnv(t)
	alice := jtx.NewAccount("alice")
	tester := mpt.NewMPTTester(t, env, alice)
	tester.Create(mpt.CreateOpts{Metadata: mpt.PtrString("74657374")})
	env.Close()

	tester.Set(mpt.SetOpts{
		Account:     alice,
		Flags:       mpt.TfMPTSetCanTransfer,
		Metadata:    mpt.PtrString("6d657461"),
		TransferFee: mpt.PtrUint16(100),
	})
	env.Close()
	tester.CheckMetadata("6d657461")
	tester.CheckTransferFee(100)
	tester.CheckIssuanceFlags(0x00000020)

	tester.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString(""), TransferFee: mpt.PtrUint16(0)})
	env.Close()
	if tester.IsMetadataPresent() || tester.IsTransferFeePresent() {
		t.Fatal("default-mutable metadata and transfer fee should be removable")
	}

	for _, flag := range []uint32{
		mpt.TfMPTSetCanLock,
		mpt.TfMPTSetRequireAuth,
		mpt.TfMPTSetCanEscrow,
		mpt.TfMPTSetCanTrade,
		mpt.TfMPTSetCanTransfer,
		mpt.TfMPTSetCanClawback,
	} {
		tester.Set(mpt.SetOpts{Account: alice, Flags: flag})
		tester.Set(mpt.SetOpts{Account: alice, Flags: flag})
	}
	env.Close()
	tester.CheckIssuanceFlags(0x0000007e)
}

func TestMPTDynamicCapabilitiesAffectTransactions(t *testing.T) {
	t.Run("CanLock", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{})
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanLock})
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTUnlock})
	})

	t.Run("RequireAuth", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		tester := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		tester.Create(mpt.CreateOpts{})
		tester.Authorize(mpt.AuthorizeOpts{Account: bob})
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetRequireAuth})
		tester.Pay(alice, bob, 100, jtx.TecNO_AUTH)
		tester.Authorize(mpt.AuthorizeOpts{Account: alice, Holder: bob})
		tester.Pay(alice, bob, 100)
	})

	t.Run("CanTransfer", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		tester := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		tester.Create(mpt.CreateOpts{})
		tester.Authorize(mpt.AuthorizeOpts{Account: bob})
		tester.Authorize(mpt.AuthorizeOpts{Account: carol})
		tester.Pay(alice, bob, 100)
		tester.Pay(bob, carol, 10, jtx.TecNO_AUTH)
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanTransfer})
		tester.Pay(bob, carol, 10)
	})

	t.Run("CanClawback", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		tester := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		tester.Create(mpt.CreateOpts{})
		tester.Authorize(mpt.AuthorizeOpts{Account: bob})
		tester.Pay(alice, bob, 100)
		tester.Claw(alice, bob, 1, jtx.TecNO_PERMISSION)
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanClawback})
		tester.Claw(alice, bob, 1)
	})

	t.Run("CanHoldConfidentialBalance", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{})
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanHoldConfidentialBalance})
		env.Close()
		tester.CheckIssuanceFlags(0x00000080)
	})
}

func TestMPTImmutableCapabilities(t *testing.T) {
	cases := []struct {
		name      string
		immutable uint32
		setFlag   uint32
	}{
		{"CanLock", mpt.TifMPTCanLock, mpt.TfMPTSetCanLock},
		{"RequireAuth", mpt.TifMPTRequireAuth, mpt.TfMPTSetRequireAuth},
		{"CanEscrow", mpt.TifMPTCanEscrow, mpt.TfMPTSetCanEscrow},
		{"CanTrade", mpt.TifMPTCanTrade, mpt.TfMPTSetCanTrade},
		{"CanTransfer", mpt.TifMPTCanTransfer, mpt.TfMPTSetCanTransfer},
		{"CanClawback", mpt.TifMPTCanClawback, mpt.TfMPTSetCanClawback},
		{"CanHoldConfidentialBalance", mpt.TifMPTCanHoldConfidentialBalance, mpt.TfMPTSetCanHoldConfidentialBalance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := dynamicMPTEnv(t)
			if tc.immutable == mpt.TifMPTCanHoldConfidentialBalance {
				env.EnableFeature("ConfidentialTransfer")
				env.Close()
			}
			alice := jtx.NewAccount("alice")
			tester := mpt.NewMPTTester(t, env, alice)
			tester.Create(mpt.CreateOpts{ImmutableFlags: mpt.PtrUint32(tc.immutable)})
			env.Close()
			tester.Set(mpt.SetOpts{Account: alice, Flags: tc.setFlag, Err: jtx.TecNO_PERMISSION})
			tester.CheckIssuanceFlags(0)
		})
	}
}

func TestMPTImmutableMetadataAndTransferFee(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{Metadata: mpt.PtrString("74657374"), ImmutableFlags: mpt.PtrUint32(mpt.TifMPTMetadata)})
		env.Close()
		for _, metadata := range []string{"74657374", "", "6d657461"} {
			tester.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString(metadata), Err: jtx.TecNO_PERMISSION})
		}
		tester.CheckMetadata("74657374")
	})

	t.Run("transfer fee", func(t *testing.T) {
		env := dynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{
			Flags:          mpt.TfMPTCanTransfer,
			TransferFee:    mpt.PtrUint16(10),
			ImmutableFlags: mpt.PtrUint32(mpt.TifMPTTransferFee),
		})
		env.Close()
		for _, fee := range []uint16{10, 0, 100} {
			tester.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(fee), Err: jtx.TecNO_PERMISSION})
		}
		tester.CheckTransferFee(10)
	})
}

func TestMPTEnableAndFreezeMonotonically(t *testing.T) {
	env := dynamicMPTEnv(t)
	alice := jtx.NewAccount("alice")
	tester := mpt.NewMPTTester(t, env, alice)
	tester.Create(mpt.CreateOpts{})
	env.Close()

	tester.Set(mpt.SetOpts{
		Account:        alice,
		Flags:          mpt.TfMPTSetCanClawback,
		ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanClawback),
	})
	env.Close()
	tester.CheckIssuanceFlags(0x00000040)
	tester.CheckImmutableFlags(mpt.TifMPTCanClawback)
	tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanClawback, Err: jtx.TecNO_PERMISSION})

	tester.Set(mpt.SetOpts{Account: alice, ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanTrade)})
	tester.Set(mpt.SetOpts{Account: alice, ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanTrade)})
	tester.Set(mpt.SetOpts{Account: alice, ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanTransfer)})
	env.Close()
	tester.CheckImmutableFlags(mpt.TifMPTCanClawback | mpt.TifMPTCanTrade | mpt.TifMPTCanTransfer)
	tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanTrade, Err: jtx.TecNO_PERMISSION})
}

func TestMPTImmutableCanLockFreezesCapabilityNotLockState(t *testing.T) {
	env := dynamicMPTEnv(t)
	alice := jtx.NewAccount("alice")
	tester := mpt.NewMPTTester(t, env, alice)
	tester.Create(mpt.CreateOpts{})
	tester.Set(mpt.SetOpts{
		Account:        alice,
		Flags:          mpt.TfMPTSetCanLock,
		ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanLock),
	})
	env.Close()
	tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
	tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTUnlock})
}

func TestMPTConfidentialTransferFeeInteractions(t *testing.T) {
	t.Run("same transaction", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{})
		env.Close()
		tester.Set(mpt.SetOpts{
			Account:     alice,
			Flags:       mpt.TfMPTSetCanHoldConfidentialBalance,
			TransferFee: mpt.PtrUint16(1),
			Err:         jtx.TemBAD_TRANSFER_FEE,
		})
	})

	t.Run("existing fee blocks confidential enable", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanTransfer, TransferFee: mpt.PtrUint16(1)})
		env.Close()
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanHoldConfidentialBalance, Err: jtx.TecNO_PERMISSION})
	})

	t.Run("existing confidential capability blocks fee", func(t *testing.T) {
		env := confidentialDynamicMPTEnv(t)
		alice := jtx.NewAccount("alice")
		tester := mpt.NewMPTTester(t, env, alice)
		tester.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanHoldConfidentialBalance})
		env.Close()
		tester.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTSetCanTransfer})
		tester.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(1), Err: jtx.TecNO_PERMISSION})
	})
}

func TestMPTImmutableFailureRollsBackAllMutations(t *testing.T) {
	env := dynamicMPTEnv(t)
	alice := jtx.NewAccount("alice")
	tester := mpt.NewMPTTester(t, env, alice)
	tester.Create(mpt.CreateOpts{
		Metadata:       mpt.PtrString("74657374"),
		ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanTrade),
	})
	env.Close()

	tester.Set(mpt.SetOpts{
		Account:        alice,
		Flags:          mpt.TfMPTSetCanTrade,
		Metadata:       mpt.PtrString("6d657461"),
		ImmutableFlags: mpt.PtrUint32(mpt.TifMPTCanLock),
		Err:            jtx.TecNO_PERMISSION,
	})
	env.Close()
	tester.CheckIssuanceFlags(0)
	tester.CheckMetadata("74657374")
	tester.CheckImmutableFlags(mpt.TifMPTCanTrade)
}

func TestMPTEnableAndFreezeMetadata(t *testing.T) {
	env := dynamicMPTEnv(t)
	alice := jtx.NewAccount("alice")
	tester := mpt.NewMPTTester(t, env, alice)
	tester.Create(mpt.CreateOpts{Metadata: mpt.PtrString("74657374")})
	env.Close()

	metadata := "6d657461"
	immutable := mpt.TifMPTMetadata
	set := mpttx.NewMPTokenIssuanceSet(alice.Address, tester.IssuanceID())
	set.Fee = "10"
	set.MPTokenMetadata = &metadata
	set.ImmutableFlags = &immutable
	result := env.Submit(set)
	jtx.RequireTxSuccess(t, result)

	modified := modifiedTypesFromBlob(t, result.Metadata)["MPTokenIssuance"]
	require.NotNil(t, modified)
	previous, ok := modified["PreviousFields"].(map[string]any)
	require.True(t, ok)
	final, ok := modified["FinalFields"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "74657374", previous["MPTokenMetadata"])
	require.Equal(t, "6D657461", final["MPTokenMetadata"])
	require.EqualValues(t, mpt.TifMPTMetadata, final["ImmutableFlags"])

	env.Close()
	tester.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString("00"), Err: jtx.TecNO_PERMISSION})
}

func TestMPTSetNoOp(t *testing.T) {
	for _, feature := range []string{"DynamicMPT", "SingleAssetVault"} {
		t.Run(feature, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature(feature)
			env.Close()
			alice := jtx.NewAccount("alice")
			tester := mpt.NewMPTTester(t, env, alice)
			tester.Create(mpt.CreateOpts{})
			env.Close()
			tester.Set(mpt.SetOpts{Account: alice, Err: jtx.TemMALFORMED})
		})
	}
}

func hexRepeat(hexByte string, count int) string {
	out := make([]byte, 0, len(hexByte)*count)
	for i := 0; i < count; i++ {
		out = append(out, hexByte...)
	}
	return string(out)
}

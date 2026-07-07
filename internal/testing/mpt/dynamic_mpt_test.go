// Tests for DynamicMPT (XLS-94d): mutable MPTokenIssuance capabilities.
// Ported from rippled MPToken_test.cpp — testInvalidCreateDynamic,
// testInvalidSetDynamic, testMutateMPT, testMutateCanLock.
//
// DynamicMPT is Supported::no, so it is not part of the env's default amendment
// set; tests that exercise mutation must enable it explicitly, while the
// temDISABLED paths run with it off.
package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
)

// newDynamicEnv returns an env with DynamicMPT enabled.
func newDynamicEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("DynamicMPT")
	env.Close()
	return env
}

// --------------------------------------------------------------------------
// TestMPT_InvalidCreateDynamic
// Reference: rippled MPToken_test.cpp testInvalidCreateDynamic()
// --------------------------------------------------------------------------

func TestMPT_InvalidCreateDynamic(t *testing.T) {
	t.Run("MutableFlagsWithoutAmendment", func(t *testing.T) {
		// DynamicMPT off: any MutableFlags value is rejected up front.
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)

		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(0),
			MutableFlags: mpt.PtrUint32(2),
			Err:          jtx.TemDISABLED,
		})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(0),
			MutableFlags: mpt.PtrUint32(0),
			Err:          jtx.TemDISABLED,
		})
	})

	t.Run("InvalidMutableFlagValues", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)

		// 1 aliases MPT lock and is not a valid create mutable flag.
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(0),
			MutableFlags: mpt.PtrUint32(1),
			Err:          jtx.TemINVALID_FLAG,
		})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(0),
			MutableFlags: mpt.PtrUint32(17),
			Err:          jtx.TemINVALID_FLAG,
		})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(0),
			MutableFlags: mpt.PtrUint32(65535),
			Err:          jtx.TemINVALID_FLAG,
		})
		// MutableFlags cannot be 0 (must name at least one capability).
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(0),
			MutableFlags: mpt.PtrUint32(0),
			Err:          jtx.TemINVALID_FLAG,
		})
	})

	t.Run("StoresMutableFlags", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)

		flags := mpt.TmfMPTCanMutateMetadata | mpt.TmfMPTCanMutateTransferFee
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(1),
			MutableFlags: mpt.PtrUint32(flags),
		})
		env.Close()
		mptAlice.CheckMutableFlags(flags)
	})
}

// --------------------------------------------------------------------------
// TestMPT_InvalidSetDynamic
// Reference: rippled MPToken_test.cpp testInvalidSetDynamic()
// --------------------------------------------------------------------------

func TestMPT_InvalidSetDynamic(t *testing.T) {
	t.Run("MutationWithoutAmendment", func(t *testing.T) {
		// DynamicMPT off: mutation fields are parsed and rejected at preflight.
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)

		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, MutableFlags: mpt.PtrUint32(2), Err: jtx.TemDISABLED})
		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, MutableFlags: mpt.PtrUint32(0), Err: jtx.TemDISABLED})
		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, Metadata: mpt.PtrString("74657374"), Err: jtx.TemDISABLED})
		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, Metadata: mpt.PtrString(""), Err: jtx.TemDISABLED})
		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, TransferFee: mpt.PtrUint16(100), Err: jtx.TemDISABLED})
		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, TransferFee: mpt.PtrUint16(0), Err: jtx.TemDISABLED})
	})

	t.Run("HolderNotAllowedWhenMutating", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)

		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, MutableFlags: mpt.PtrUint32(2), Err: jtx.TemMALFORMED})
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, Metadata: mpt.PtrString("74657374"), Err: jtx.TemMALFORMED})
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, ID: id, TransferFee: mpt.PtrUint16(100), Err: jtx.TemMALFORMED})
	})

	t.Run("FlagsNotAllowedWhenMutating", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateMetadata |
				mpt.TmfMPTCanMutateCanLock | mpt.TmfMPTCanMutateTransferFee),
		})
		env.Close()

		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTCanLock, MutableFlags: mpt.PtrUint32(2), Err: jtx.TemMALFORMED})
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTCanLock, Metadata: mpt.PtrString("74657374"), Err: jtx.TemMALFORMED})
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTCanLock, TransferFee: mpt.PtrUint16(100), Err: jtx.TemMALFORMED})
	})

	t.Run("ZeroOrCanonicalSigFlagsAreFine", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			TransferFee:  mpt.PtrUint16(10),
			OwnerCount:   mpt.PtrUint32(1),
			Flags:        mpt.TfMPTCanTransfer,
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateTransferFee | mpt.TmfMPTCanMutateMetadata),
		})
		env.Close()

		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: 0, TransferFee: mpt.PtrUint16(100), Metadata: mpt.PtrString("74657374")})
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: 0x80000000, TransferFee: mpt.PtrUint16(200), Metadata: mpt.PtrString("7465737432")})
	})

	t.Run("InvalidMutableFlagValues", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)

		for _, flags := range []uint32{10000, 0, 5000} {
			mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, MutableFlags: mpt.PtrUint32(flags), Err: jtx.TemINVALID_FLAG})
		}
	})

	t.Run("CannotSetAndClearSameFlag", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)

		combos := []uint32{
			mpt.TmfMPTSetCanLock | mpt.TmfMPTClearCanLock,
			mpt.TmfMPTSetRequireAuth | mpt.TmfMPTClearRequireAuth,
			mpt.TmfMPTSetCanEscrow | mpt.TmfMPTClearCanEscrow,
			mpt.TmfMPTSetCanTrade | mpt.TmfMPTClearCanTrade,
			mpt.TmfMPTSetCanTransfer | mpt.TmfMPTClearCanTransfer,
			mpt.TmfMPTSetCanClawback | mpt.TmfMPTClearCanClawback,
			mpt.TmfMPTSetCanLock | mpt.TmfMPTClearCanLock | mpt.TmfMPTClearCanTrade,
			mpt.TmfMPTSetCanTransfer | mpt.TmfMPTClearCanTransfer |
				mpt.TmfMPTSetCanEscrow | mpt.TmfMPTClearCanClawback,
		}
		for _, mf := range combos {
			mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, MutableFlags: mpt.PtrUint32(mf), Err: jtx.TemINVALID_FLAG})
		}
	})

	t.Run("CannotMutateNonMutableFlag", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})
		env.Close()

		flags := []uint32{
			mpt.TmfMPTSetCanLock, mpt.TmfMPTClearCanLock,
			mpt.TmfMPTSetRequireAuth, mpt.TmfMPTClearRequireAuth,
			mpt.TmfMPTSetCanEscrow, mpt.TmfMPTClearCanEscrow,
			mpt.TmfMPTSetCanTrade, mpt.TmfMPTClearCanTrade,
			mpt.TmfMPTSetCanTransfer, mpt.TmfMPTClearCanTransfer,
			mpt.TmfMPTSetCanClawback, mpt.TmfMPTClearCanClawback,
		}
		for _, mf := range flags {
			mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mf), Err: jtx.TecNO_PERMISSION})
		}
	})

	t.Run("MetadataExceedsMaxLength", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateMetadata)})
		env.Close()

		// 1025 bytes of metadata (2050 hex chars) exceeds the 1024-byte cap.
		mptAlice.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString(hexRepeat("61", 1025)), Err: jtx.TemMALFORMED})
	})

	t.Run("CannotMutateMetadataWhenNotMutable", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})
		env.Close()

		mptAlice.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString("74657374"), Err: jtx.TecNO_PERMISSION})
	})

	t.Run("TransferFeeExceedsMax", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateTransferFee)})
		env.Close()

		mptAlice.Set(mpt.SetOpts{Account: alice, ID: id, TransferFee: mpt.PtrUint16(50001), Err: jtx.TemBAD_TRANSFER_FEE})
	})

	t.Run("SetFeeAndClearCanTransfer", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			TransferFee:  mpt.PtrUint16(100),
			OwnerCount:   mpt.PtrUint32(1),
			Flags:        mpt.TfMPTCanTransfer,
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateTransferFee | mpt.TmfMPTCanMutateCanTransfer),
		})
		env.Close()

		// A non-zero fee together with clearing MPTCanTransfer is contradictory.
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTClearCanTransfer), TransferFee: mpt.PtrUint16(1), Err: jtx.TemMALFORMED})

		// fee 0 + clear MPTCanTransfer succeeds: the fee field is removed.
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTClearCanTransfer), TransferFee: mpt.PtrUint16(0)})
		env.Close()
		if mptAlice.IsTransferFeePresent() {
			t.Fatal("expected TransferFee to be removed")
		}
	})

	t.Run("CannotSetFeeWhenCanTransferNotSet", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:   mpt.PtrUint32(1),
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateTransferFee | mpt.TmfMPTCanMutateCanTransfer),
		})
		env.Close()

		mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(100), Err: jtx.TecNO_PERMISSION})
		// Enabling MPTCanTransfer in the same tx does not satisfy the requirement.
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTSetCanTransfer), TransferFee: mpt.PtrUint16(100), Err: jtx.TecNO_PERMISSION})
	})

	t.Run("CannotMutateTransferFeeWhenNotMutable", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{TransferFee: mpt.PtrUint16(10), OwnerCount: mpt.PtrUint32(1), Flags: mpt.TfMPTCanTransfer})
		env.Close()

		mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(100), Err: jtx.TecNO_PERMISSION})
		mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(0), Err: jtx.TecNO_PERMISSION})
	})

	t.Run("PartialMutability", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateCanTrade |
				mpt.TmfMPTCanMutateCanTransfer | mpt.TmfMPTCanMutateMetadata),
		})
		env.Close()

		// TransferFee is not mutable here.
		mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(100), Err: jtx.TecNO_PERMISSION})

		nonMutable := []uint32{
			mpt.TmfMPTSetCanLock, mpt.TmfMPTClearCanLock,
			mpt.TmfMPTSetRequireAuth, mpt.TmfMPTClearRequireAuth,
			mpt.TmfMPTSetCanEscrow, mpt.TmfMPTClearCanEscrow,
			mpt.TmfMPTSetCanClawback, mpt.TmfMPTClearCanClawback,
		}
		for _, mf := range nonMutable {
			mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mf), Err: jtx.TecNO_PERMISSION})
		}

		// The mutable capabilities succeed.
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTSetCanTrade)})
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTClearCanTrade)})
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTSetCanTransfer)})
		mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTClearCanTransfer)})
		mptAlice.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString("74657374")})
		mptAlice.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString("")})
	})
}

// --------------------------------------------------------------------------
// TestMPT_MutateMPT — successful metadata/fee/flag mutations update the SLE.
// Reference: rippled MPToken_test.cpp testMutateMPT()
// --------------------------------------------------------------------------

func TestMPT_MutateMPT(t *testing.T) {
	t.Run("MutateMetadata", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)
		mptAlice.Create(mpt.CreateOpts{
			Metadata:     mpt.PtrString("74657374"),
			OwnerCount:   mpt.PtrUint32(1),
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateMetadata),
		})
		env.Close()

		for _, meta := range []string{"6d657461", "6d65746132", "74657374", "6d657461"} {
			mptAlice.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString(meta)})
			env.Close()
			mptAlice.CheckMetadata(meta)
		}

		// Empty metadata removes the field.
		mptAlice.Set(mpt.SetOpts{Account: alice, Metadata: mpt.PtrString("")})
		env.Close()
		if mptAlice.IsMetadataPresent() {
			t.Fatal("expected metadata to be removed")
		}
	})

	t.Run("MutateTransferFee", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)
		mptAlice.Create(mpt.CreateOpts{
			TransferFee:  mpt.PtrUint16(100),
			Metadata:     mpt.PtrString("74657374"),
			OwnerCount:   mpt.PtrUint32(1),
			Flags:        mpt.TfMPTCanTransfer,
			MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateTransferFee),
		})
		env.Close()

		for _, fee := range []uint16{1, 10, 100, 200, 500, 1000, 50000} {
			mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(fee)})
			env.Close()
			mptAlice.CheckTransferFee(fee)
		}

		// Setting fee to 0 removes the field, then re-setting works.
		mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(0)})
		env.Close()
		if mptAlice.IsTransferFeePresent() {
			t.Fatal("expected TransferFee to be removed")
		}
		mptAlice.Set(mpt.SetOpts{Account: alice, TransferFee: mpt.PtrUint16(10)})
		env.Close()
		mptAlice.CheckTransferFee(10)
	})

	t.Run("FlagToggling", func(t *testing.T) {
		cases := []struct {
			name        string
			createFlags uint32
			setFlag     uint32
			clearFlag   uint32
			lsf         uint32
		}{
			{"RequireAuth", mpt.TmfMPTCanMutateRequireAuth, mpt.TmfMPTSetRequireAuth, mpt.TmfMPTClearRequireAuth, 0x00000004},
			{"CanEscrow", mpt.TmfMPTCanMutateCanEscrow, mpt.TmfMPTSetCanEscrow, mpt.TmfMPTClearCanEscrow, 0x00000008},
			{"CanTrade", mpt.TmfMPTCanMutateCanTrade, mpt.TmfMPTSetCanTrade, mpt.TmfMPTClearCanTrade, 0x00000010},
			{"CanTransfer", mpt.TmfMPTCanMutateCanTransfer, mpt.TmfMPTSetCanTransfer, mpt.TmfMPTClearCanTransfer, 0x00000020},
			{"CanClawback", mpt.TmfMPTCanMutateCanClawback, mpt.TmfMPTSetCanClawback, mpt.TmfMPTClearCanClawback, 0x00000040},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				env := newDynamicEnv(t)
				alice := jtx.NewAccount("alice")
				mptAlice := mpt.NewMPTTester(t, env, alice)
				mptAlice.Create(mpt.CreateOpts{
					Metadata:     mpt.PtrString("74657374"),
					OwnerCount:   mpt.PtrUint32(1),
					MutableFlags: mpt.PtrUint32(tc.createFlags),
				})
				env.Close()
				// The mutable-permission bit is stored, no lsf flag yet.
				mptAlice.CheckIssuanceFlags(0)

				mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(tc.setFlag)})
				env.Close()
				mptAlice.CheckIssuanceFlags(tc.lsf)

				mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(tc.clearFlag)})
				env.Close()
				mptAlice.CheckIssuanceFlags(0)
			})
		}
	})
}

// --------------------------------------------------------------------------
// TestMPT_MutateCanLock — lock/unlock is gated by a mutable CanLock flag.
// Reference: rippled MPToken_test.cpp testMutateCanLock()
// --------------------------------------------------------------------------

func TestMPT_MutateCanLock(t *testing.T) {
	env := newDynamicEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
	mptAlice.Create(mpt.CreateOpts{
		OwnerCount: mpt.PtrUint32(1),
		Flags:      mpt.TfMPTCanLock,
		MutableFlags: mpt.PtrUint32(mpt.TmfMPTCanMutateCanLock |
			mpt.TmfMPTCanMutateCanClawback | mpt.TmfMPTCanMutateMetadata),
	})
	env.Close()
	mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob, HolderCount: mpt.PtrUint32(1)})
	env.Close()

	// Lock and unlock work while CanLock is set.
	mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
	mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})
	mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTUnlock})
	mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTUnlock})
	env.Close()

	// Clear lsfMPTCanLock via a mutation.
	mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTClearCanLock)})
	env.Close()

	// Lock/unlock now fail.
	mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock, Err: jtx.TecNO_PERMISSION})
	mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTUnlock, Err: jtx.TecNO_PERMISSION})
	mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock, Err: jtx.TecNO_PERMISSION})
	mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTUnlock, Err: jtx.TecNO_PERMISSION})

	// Restore CanLock and confirm lock/unlock works again.
	mptAlice.Set(mpt.SetOpts{Account: alice, MutableFlags: mpt.PtrUint32(mpt.TmfMPTSetCanLock)})
	env.Close()
	mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
	mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})
	mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTUnlock})
	mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTUnlock})
}

// --------------------------------------------------------------------------
// TestMPT_SetNoOp — under SingleAssetVault or DynamicMPT a Set that changes
// nothing is malformed. Reference: rippled MPTokenIssuanceSet.cpp preflight.
// --------------------------------------------------------------------------

func TestMPT_SetNoOp(t *testing.T) {
	t.Run("UnderDynamicMPT", func(t *testing.T) {
		env := newDynamicEnv(t)
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})
		env.Close()
		mptAlice.Set(mpt.SetOpts{Account: alice, Err: jtx.TemMALFORMED})
	})

	t.Run("UnderSingleAssetVault", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.EnableFeature("SingleAssetVault")
		env.Close()
		alice := jtx.NewAccount("alice")
		mptAlice := mpt.NewMPTTester(t, env, alice)
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})
		env.Close()
		mptAlice.Set(mpt.SetOpts{Account: alice, Err: jtx.TemMALFORMED})
	})
}

// hexRepeat returns the hex byte pattern repeated count times.
func hexRepeat(hexByte string, count int) string {
	out := make([]byte, 0, len(hexByte)*count)
	for i := 0; i < count; i++ {
		out = append(out, hexByte...)
	}
	return string(out)
}

package accountset

import (
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
)

func TestAccountSetBuilder(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	t.Run("defaults", func(t *testing.T) {
		built := AccountSet(alice).Build()
		require.IsType(t, &accounttx.AccountSet{}, built)
		require.Equal(t, alice.Address, built.Account)
		require.Empty(t, built.Fee)
		require.Nil(t, built.SetFlag)
		require.Nil(t, built.ClearFlag)
		require.Nil(t, built.Domain)
		require.Empty(t, built.EmailHash)
		require.Nil(t, built.MessageKey)
		require.Empty(t, built.WalletLocator)
		require.Nil(t, built.TransferRate)
		require.Nil(t, built.TickSize)
		require.Empty(t, built.NFTokenMinter)
		require.Nil(t, built.Sequence)
		require.Zero(t, built.GetFlags())
	})

	t.Run("scalar fields", func(t *testing.T) {
		built := AccountSet(alice).
			SetFlag(1).
			ClearFlag(2).
			TransferRate(1_100_000_000).
			TickSize(16).
			Fee(25).
			Sequence(7).
			TxFlags(accounttx.AccountSetTxFlagAllowXRP).
			Build()

		require.Equal(t, uint32(1), *built.SetFlag)
		require.Equal(t, uint32(2), *built.ClearFlag)
		require.Equal(t, uint32(1_100_000_000), *built.TransferRate)
		require.Equal(t, uint8(16), *built.TickSize)
		require.Equal(t, "25", built.Fee)
		require.Equal(t, uint32(7), *built.Sequence)
		require.Equal(t, accounttx.AccountSetTxFlagAllowXRP, built.GetFlags())
	})

	t.Run("explicit zero values", func(t *testing.T) {
		built := AccountSet(alice).
			SetFlag(0).
			ClearFlag(0).
			TransferRate(0).
			TickSize(0).
			Fee(0).
			Sequence(0).
			TxFlags(0).
			Build()

		require.NotNil(t, built.SetFlag)
		require.Zero(t, *built.SetFlag)
		require.NotNil(t, built.ClearFlag)
		require.Zero(t, *built.ClearFlag)
		require.NotNil(t, built.TransferRate)
		require.Zero(t, *built.TransferRate)
		require.NotNil(t, built.TickSize)
		require.Zero(t, *built.TickSize)
		require.Equal(t, "0", built.Fee)
		require.NotNil(t, built.Sequence)
		require.Zero(t, *built.Sequence)
		require.Zero(t, built.GetFlags())
	})

	fieldTests := []struct {
		name  string
		build func() *accounttx.AccountSet
		check func(*testing.T, *accounttx.AccountSet)
	}{
		{
			name:  "domain value",
			build: func() *accounttx.AccountSet { return AccountSet(alice).Domain("ABCD").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) { require.Equal(t, "ABCD", *built.Domain) },
		},
		{
			name:  "domain clear presence",
			build: func() *accounttx.AccountSet { return AccountSet(alice).Domain("").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) { require.Equal(t, "", *built.Domain) },
		},
		{
			name: "email hash value",
			build: func() *accounttx.AccountSet {
				return AccountSet(alice).EmailHash("0123456789ABCDEF0123456789ABCDEF").Build()
			},
			check: func(t *testing.T, built *accounttx.AccountSet) {
				require.Equal(t, "0123456789ABCDEF0123456789ABCDEF", built.EmailHash)
			},
		},
		{
			name:  "email hash clear",
			build: func() *accounttx.AccountSet { return AccountSet(alice).EmailHash("").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) {
				require.Equal(t, "00000000000000000000000000000000", built.EmailHash)
			},
		},
		{
			name:  "message key value",
			build: func() *accounttx.AccountSet { return AccountSet(alice).MessageKey("ABCD").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) { require.Equal(t, "ABCD", *built.MessageKey) },
		},
		{
			name:  "message key clear presence",
			build: func() *accounttx.AccountSet { return AccountSet(alice).MessageKey("").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) { require.Equal(t, "", *built.MessageKey) },
		},
		{
			name:  "wallet locator value",
			build: func() *accounttx.AccountSet { return AccountSet(alice).WalletLocator("ABCD").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) { require.Equal(t, "ABCD", built.WalletLocator) },
		},
		{
			name:  "wallet locator clear",
			build: func() *accounttx.AccountSet { return AccountSet(alice).WalletLocator("").Build() },
			check: func(t *testing.T, built *accounttx.AccountSet) {
				require.Equal(t, "0000000000000000000000000000000000000000000000000000000000000000", built.WalletLocator)
			},
		},
	}

	for _, tt := range fieldTests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.build())
		})
	}

	flagTests := []struct {
		name string
		flag uint32
		set  func(*AccountSetBuilder) *AccountSetBuilder
	}{
		{name: "require destination", flag: accounttx.AccountSetFlagRequireDest, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.RequireDest() }},
		{name: "require authorization", flag: accounttx.AccountSetFlagRequireAuth, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.RequireAuth() }},
		{name: "disallow XRP", flag: accounttx.AccountSetFlagDisallowXRP, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.DisallowXRP() }},
		{name: "default ripple", flag: accounttx.AccountSetFlagDefaultRipple, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.DefaultRipple() }},
		{name: "deposit auth", flag: accounttx.AccountSetFlagDepositAuth, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.DepositAuth() }},
		{name: "no freeze", flag: accounttx.AccountSetFlagNoFreeze, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.NoFreeze() }},
		{name: "global freeze", flag: accounttx.AccountSetFlagGlobalFreeze, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.GlobalFreeze() }},
		{name: "allow clawback", flag: accounttx.AccountSetFlagAllowTrustLineClawback, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.AllowClawback() }},
		{name: "allow trust line locking", flag: accounttx.AccountSetFlagAllowTrustLineLocking, set: func(b *AccountSetBuilder) *AccountSetBuilder { return b.AllowTrustLineLocking() }},
	}

	for _, tt := range flagTests {
		t.Run(tt.name, func(t *testing.T) {
			built := tt.set(AccountSet(alice)).Build()
			require.Equal(t, tt.flag, *built.SetFlag)
		})
	}

	t.Run("authorized minter", func(t *testing.T) {
		built := AccountSet(alice).AuthorizedMinter(bob).Build()
		require.Equal(t, accounttx.AccountSetFlagAuthorizedNFTokenMinter, *built.SetFlag)
		require.Equal(t, bob.Address, built.NFTokenMinter)
	})
}

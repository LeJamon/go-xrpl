package engine

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

func TestVaultDeleteMemoDataPreflightPrecedence(t *testing.T) {
	off := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	on := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureLendingProtocolV1_1,
	})
	validVaultID := strings.Repeat("01", 32)

	newDelete := func(rules *amendment.Rules) (*Engine, *vault.VaultDelete) {
		deleteTx := vault.NewVaultDelete(precedenceSourceAddr, validVaultID)
		deleteTx.Fee = "10"
		deleteTx.Sequence = u32(5)
		deleteTx.MemoData = "AA"
		return preflightEngine(rules), deleteTx
	}

	t.Run("bad fee precedes amendment gate", func(t *testing.T) {
		engine, deleteTx := newDelete(off)
		deleteTx.Fee = "-1"
		if got := engine.preflight(deleteTx); got != ter.TemBAD_FEE {
			t.Fatalf("preflight() = %v, want temBAD_FEE", got)
		}
	})

	t.Run("invalid flags precede amendment gate", func(t *testing.T) {
		engine, deleteTx := newDelete(off)
		flags := uint32(0x00010000)
		deleteTx.Flags = &flags
		if got := engine.preflight(deleteTx); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflight() = %v, want temINVALID_FLAG", got)
		}
	})

	t.Run("zero VaultID precedes amendment gate", func(t *testing.T) {
		engine, deleteTx := newDelete(off)
		deleteTx.VaultID = strings.Repeat("00", 32)
		if got := engine.preflight(deleteTx); got != ter.TemMALFORMED {
			t.Fatalf("preflight() = %v, want temMALFORMED", got)
		}
	})

	t.Run("amendment gate precedes length", func(t *testing.T) {
		engine, deleteTx := newDelete(off)
		deleteTx.MemoData = strings.Repeat("AA", vault.MaxVaultDataLength+1)
		if got := engine.preflight(deleteTx); got != ter.TemDISABLED {
			t.Fatalf("preflight() = %v, want temDISABLED", got)
		}
	})

	t.Run("enabled amendment reaches length check", func(t *testing.T) {
		engine, deleteTx := newDelete(on)
		deleteTx.MemoData = strings.Repeat("AA", vault.MaxVaultDataLength+1)
		if got := engine.preflight(deleteTx); got != ter.TemMALFORMED {
			t.Fatalf("preflight() = %v, want temMALFORMED", got)
		}
	})

	t.Run("absent field does not require amendment", func(t *testing.T) {
		engine, deleteTx := newDelete(off)
		deleteTx.MemoData = ""
		if got := engine.preflight(deleteTx); got != ter.TesSUCCESS {
			t.Fatalf("preflight() = %v, want tesSUCCESS", got)
		}
	})
}

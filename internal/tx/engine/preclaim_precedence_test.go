package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Genesis keypair: the master-passphrase public key derives to this address, so
// signing with it is authorized only while the account's master key is enabled.
const (
	precedenceGenesisAddr   = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	precedenceGenesisPubKey = "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020"
	// A second, unrelated account used as the transaction source.
	precedenceSourceAddr = "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"
)

// precedenceEngine builds an engine over a mock view with all supported
// amendments enabled and an open ledger (so the fee floor is active).
func precedenceEngine(t *testing.T, accounts map[string]*state.AccountRoot) *Engine {
	return precedenceEngineWithRules(t, accounts, amendment.AllSupportedRules())
}

func precedenceEngineWithRules(t *testing.T, accounts map[string]*state.AccountRoot, rules *amendment.Rules) *Engine {
	t.Helper()
	base := newMockBaseView()
	for addr, root := range accounts {
		id, err := state.DecodeAccountID(addr)
		if err != nil {
			t.Fatalf("decode %s: %v", addr, err)
		}
		data, err := state.SerializeAccountRoot(root)
		if err != nil {
			t.Fatalf("serialize %s: %v", addr, err)
		}
		base.data[keylet.Account(id).Key] = data
	}
	return NewEngine(base, txcore.EngineConfig{
		BaseFee:        10,
		OpenLedger:     true,
		LedgerSequence: 100,
		Rules:          rules,
	})
}

func u32(v uint32) *uint32 { return &v }

// TestPreclaimPrecedence_SignBeforeFee pins the invoke_preclaim ordering change
// (rippled PR #6192): the signature check runs before the fee check, so a
// transaction that fails BOTH signature verification and the fee check surfaces
// the signature failure — never a fee-charging TER. Reference: rippled
// applySteps.cpp invoke_preclaim.
func TestPreclaimPrecedence_SignBeforeFee(t *testing.T) {
	// masterDisabled + zero balance: checkSign yields tefMASTER_DISABLED and
	// checkFee yields terINSUF_FEE_B. The reorder must return the signature code.
	makeTx := func() *txcore.BaseTx {
		tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceGenesisAddr)
		tx.Fee = "100"
		tx.Sequence = u32(5)
		tx.SigningPubKey = precedenceGenesisPubKey
		return tx
	}

	t.Run("bad sign + insufficient fee returns sign error", func(t *testing.T) {
		e := precedenceEngine(t, map[string]*state.AccountRoot{
			precedenceGenesisAddr: {
				Account:  precedenceGenesisAddr,
				Balance:  0, // below the 100-drop fee
				Sequence: 5,
				Flags:    state.LsfDisableMaster, // master signing not allowed
			},
		})
		if got := e.preclaim(makeTx(), [32]byte{}); got != ter.TefMASTER_DISABLED {
			t.Fatalf("combined sign+fee failure = %v, want TefMASTER_DISABLED", got)
		}
	})

	t.Run("fee check still fires when signature is authorized", func(t *testing.T) {
		// Master enabled → checkSign passes; zero balance → checkFee fails.
		e := precedenceEngine(t, map[string]*state.AccountRoot{
			precedenceGenesisAddr: {
				Account:  precedenceGenesisAddr,
				Balance:  0,
				Sequence: 5,
			},
		})
		if got := e.preclaim(makeTx(), [32]byte{}); got != ter.TerINSUF_FEE_B {
			t.Fatalf("fee-only failure = %v, want TerINSUF_FEE_B", got)
		}
	})

	t.Run("sign check still fires when fee is affordable", func(t *testing.T) {
		// Master disabled → checkSign fails; ample balance → checkFee passes.
		e := precedenceEngine(t, map[string]*state.AccountRoot{
			precedenceGenesisAddr: {
				Account:  precedenceGenesisAddr,
				Balance:  1_000_000,
				Sequence: 5,
				Flags:    state.LsfDisableMaster,
			},
		})
		if got := e.preclaim(makeTx(), [32]byte{}); got != ter.TefMASTER_DISABLED {
			t.Fatalf("sign-only failure = %v, want TefMASTER_DISABLED", got)
		}
	})
}

// TestPreclaimPrecedence_PermissionBeforeSignAndFee pins delegated permission
// ahead of signature authorization and fee checks.
func TestPreclaimPrecedence_PermissionBeforeSignAndFee(t *testing.T) {
	// Source delegates to the genesis account. No Delegate SLE exists, so
	// checkPermission yields terNO_DELEGATE_PERMISSION. Signature is verified
	// against the delegate (genesis); disabling its master key makes checkSign
	// yield tefMASTER_DISABLED.
	makeTx := func() *txcore.BaseTx {
		tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		tx.Fee = "10"
		tx.Sequence = u32(5)
		tx.SigningPubKey = precedenceGenesisPubKey
		tx.Delegate = precedenceGenesisAddr
		return tx
	}
	source := &state.AccountRoot{Account: precedenceSourceAddr, Balance: 1_000_000, Sequence: 5}

	t.Run("bad sign + no delegate permission returns permission error", func(t *testing.T) {
		e := precedenceEngine(t, map[string]*state.AccountRoot{
			precedenceSourceAddr: source,
			precedenceGenesisAddr: {
				Account:  precedenceGenesisAddr,
				Balance:  1_000_000,
				Sequence: 1,
				Flags:    state.LsfDisableMaster,
			},
		})
		if got := e.preclaim(makeTx(), [32]byte{}); got != ter.TerNO_DELEGATE_PERMISSION {
			t.Fatalf("combined sign+permission failure = %v, want TerNO_DELEGATE_PERMISSION", got)
		}
	})

	t.Run("permission check still fires when signature is authorized", func(t *testing.T) {
		// Delegate master enabled → checkSign passes; missing Delegate SLE →
		// checkPermission fails.
		e := precedenceEngine(t, map[string]*state.AccountRoot{
			precedenceSourceAddr: source,
			precedenceGenesisAddr: {
				Account:  precedenceGenesisAddr,
				Balance:  1_000_000,
				Sequence: 1,
			},
		})
		if got := e.preclaim(makeTx(), [32]byte{}); got != ter.TerNO_DELEGATE_PERMISSION {
			t.Fatalf("permission-only failure = %v, want TerNO_DELEGATE_PERMISSION", got)
		}
	})

	t.Run("insufficient fee + no delegate permission returns permission error", func(t *testing.T) {
		tx := makeTx()
		tx.Fee = "100"
		e := precedenceEngine(t, map[string]*state.AccountRoot{
			precedenceSourceAddr: source,
			precedenceGenesisAddr: {
				Account:  precedenceGenesisAddr,
				Balance:  0,
				Sequence: 1,
			},
		})
		if got := e.preclaim(tx, [32]byte{}); got != ter.TerNO_DELEGATE_PERMISSION {
			t.Fatalf("combined fee+permission failure = %v, want TerNO_DELEGATE_PERMISSION", got)
		}
	})
}

type batchSignerPrecedenceTx struct {
	txcore.BaseTx
	signers []txcore.BatchSignerInfo
}

func (t *batchSignerPrecedenceTx) GetBatchSigners() []txcore.BatchSignerInfo {
	return t.signers
}

func TestPreclaimPrecedence_PermissionBeforeBatchSignerAuthorization(t *testing.T) {
	base := txcore.NewBaseTx(txcore.TypeBatch, precedenceSourceAddr)
	base.Fee = "10"
	base.Sequence = u32(5)
	base.SigningPubKey = precedenceGenesisPubKey
	base.Delegate = precedenceGenesisAddr
	txn := &batchSignerPrecedenceTx{
		BaseTx: *base,
		signers: []txcore.BatchSignerInfo{{
			Account:       precedenceSourceAddr,
			SigningPubKey: precedenceGenesisPubKey,
		}},
	}
	e := precedenceEngine(t, map[string]*state.AccountRoot{
		precedenceSourceAddr: {
			Account:  precedenceSourceAddr,
			Balance:  1_000_000,
			Sequence: 5,
		},
		precedenceGenesisAddr: {
			Account:  precedenceGenesisAddr,
			Balance:  1_000_000,
			Sequence: 1,
		},
	})

	if got := e.preclaim(txn, [32]byte{}); got != ter.TerNO_DELEGATE_PERMISSION {
		t.Fatalf("combined Batch signer+permission failure = %v, want TerNO_DELEGATE_PERMISSION", got)
	}
}

func TestPreclaimInnerPrecedence_PermissionBeforePseudoAccountAuthorization(t *testing.T) {
	txn := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
	txn.Fee = "0"
	txn.Sequence = u32(5)
	txn.SigningPubKey = ""
	txn.Delegate = precedenceGenesisAddr

	pseudoID := [32]byte{1}
	e := precedenceEngine(t, map[string]*state.AccountRoot{
		precedenceSourceAddr: {
			Account:  precedenceSourceAddr,
			Balance:  1_000_000,
			Sequence: 5,
		},
		precedenceGenesisAddr: {
			Account:  precedenceGenesisAddr,
			Balance:  1_000_000,
			Sequence: 1,
			AMMID:    pseudoID,
		},
	})

	if got := e.preclaimInner(txn, [32]byte{1}); got != ter.TerNO_DELEGATE_PERMISSION {
		t.Fatalf("combined pseudo delegate+permission failure = %v, want TerNO_DELEGATE_PERMISSION", got)
	}
}

func TestBatchSignerPseudoAccountAuthorizationRejected(t *testing.T) {
	pseudoID := [32]byte{1}
	e := precedenceEngine(t, map[string]*state.AccountRoot{
		precedenceGenesisAddr: {
			Account:  precedenceGenesisAddr,
			Balance:  1_000_000,
			Sequence: 1,
			AMMID:    pseudoID,
		},
	})

	got := e.checkBatchSign([]txcore.BatchSignerInfo{{
		Account:       precedenceGenesisAddr,
		SigningPubKey: precedenceGenesisPubKey,
	}})
	if got != ter.TefBAD_AUTH {
		t.Fatalf("pseudo-account Batch signer = %v, want TefBAD_AUTH", got)
	}
}

func TestPseudoAccountSigningAmendmentGates(t *testing.T) {
	pseudoID := [32]byte{1}
	accounts := map[string]*state.AccountRoot{
		precedenceGenesisAddr: {
			Account:  precedenceGenesisAddr,
			Balance:  1_000_000,
			Sequence: 1,
			AMMID:    pseudoID,
		},
	}

	tests := []struct {
		name  string
		rules *amendment.Rules
		want  ter.Result
	}{
		{name: "legacy", rules: amendment.NewRules([][32]byte{amendment.FeatureSponsor}), want: ter.TesSUCCESS},
		{name: "lending", rules: amendment.NewRules([][32]byte{amendment.FeatureSponsor, amendment.FeatureLendingProtocol}), want: ter.TefBAD_AUTH},
		{name: "batch v1.1", rules: amendment.NewRules([][32]byte{amendment.FeatureSponsor, amendment.FeatureBatchV1_1}), want: ter.TefBAD_AUTH},
		{name: "cleanup", rules: amendment.NewRules([][32]byte{amendment.FeatureSponsor, amendment.FeatureFixCleanup3_3_0}), want: ter.TefBAD_AUTH},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := precedenceEngineWithRules(t, accounts, test.rules)
			if got := e.checkPseudoAccount(precedenceGenesisAddr); got != test.want {
				t.Fatalf("direct pseudo-account signing gate = %v, want %v", got, test.want)
			}

			got := e.checkBatchSign([]txcore.BatchSignerInfo{{
				Account:       precedenceGenesisAddr,
				SigningPubKey: precedenceGenesisPubKey,
			}})
			if got != test.want {
				t.Fatalf("Batch signer pseudo-account gate = %v, want %v", got, test.want)
			}

			txn := newAccountSet(precedenceSourceAddr)
			common := txn.GetCommon()
			common.Sponsor = precedenceGenesisAddr
			common.SponsorSignature = &txcore.SponsorSignature{SigningPubKey: precedenceGenesisPubKey}
			if got := e.checkSign(txn, common); got != test.want {
				t.Fatalf("Sponsor signer pseudo-account gate = %v, want %v", got, test.want)
			}
		})
	}
}

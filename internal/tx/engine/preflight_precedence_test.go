package engine

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// preflightEngine builds an engine over an empty mock view with the given rules
// and signature verification skipped, so a structurally clean transaction runs
// the full preflight ordering and returns tesSUCCESS. Every precedence test
// below asserts the TER a specific field combination surfaces, which is fixed by
// the pre-signature preflight order regardless of the (absent) signature.
func preflightEngine(rules *amendment.Rules) *Engine {
	return NewEngine(newMockBaseView(), txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     rules,
		SkipSignatureVerification: true,
	})
}

func allRules() *amendment.Rules { return amendment.AllSupportedRules() }

func rulesWithout(name string) *amendment.Rules {
	return amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).DisableByName(name).Build()
}

func TestSponsorFieldsAmendmentGate(t *testing.T) {
	sponsorRules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureSponsor).Build()
	disabledTests := []struct {
		name   string
		mutate func(*txcore.Common)
	}{
		{"Sponsor", func(common *txcore.Common) { common.Sponsor = precedenceGenesisAddr }},
		{"empty Sponsor", func(common *txcore.Common) { common.SetPresentFields(map[string]bool{"Sponsor": true}) }},
		{"SponsorFlags", func(common *txcore.Common) { flags := txcore.SpfSponsorFee; common.SponsorFlags = &flags }},
		{"SponsorSignature", func(common *txcore.Common) { common.SponsorSignature = &txcore.SponsorSignature{} }},
	}
	for _, test := range disabledTests {
		t.Run(test.name, func(t *testing.T) {
			disabled := newAccountSet(precedenceSourceAddr)
			test.mutate(disabled.GetCommon())
			if got := preflightEngine(allRules()).preflight(disabled); got != ter.TemDISABLED {
				t.Fatalf("preflight with Sponsor disabled = %v, want temDISABLED", got)
			}
		})
	}

	valid := newAccountSet(precedenceSourceAddr)
	valid.Sponsor = precedenceGenesisAddr
	flags := txcore.SpfSponsorFee
	valid.SponsorFlags = &flags
	if got := preflightEngine(sponsorRules).preflight(valid); got != ter.TesSUCCESS {
		t.Fatalf("preflight with complete Sponsor definition = %v, want tesSUCCESS", got)
	}

	inner := newAccountSet(precedenceSourceAddr)
	inner.Sponsor = precedenceGenesisAddr
	if got := preflightEngine(allRules()).preflightInner(inner); got != ter.TemDISABLED {
		t.Fatalf("inner preflight with Sponsor disabled = %v, want temDISABLED", got)
	}
}

func TestSponsorFieldsPreflight(t *testing.T) {
	sponsorRules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureSponsor).Build()
	sponsor := func(common *txcore.Common, flags uint32) {
		common.Sponsor = precedenceGenesisAddr
		common.SponsorFlags = &flags
	}
	tests := []struct {
		name   string
		tx     *txcore.BaseTx
		mutate func(*txcore.Common)
		want   ter.Result
	}{
		{
			name:   "Sponsor without flags",
			tx:     newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) { common.Sponsor = precedenceGenesisAddr },
			want:   ter.TemINVALID_FLAG,
		},
		{
			name: "explicit empty Sponsor without flags",
			tx:   newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) {
				common.SetPresentFields(map[string]bool{"Sponsor": true})
			},
			want: ter.TemINVALID_FLAG,
		},
		{
			name: "flags without Sponsor",
			tx:   newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) {
				flags := txcore.SpfSponsorFee
				common.SponsorFlags = &flags
			},
			want: ter.TemINVALID_FLAG,
		},
		{
			name:   "signature without Sponsor definition",
			tx:     newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) { common.SponsorSignature = &txcore.SponsorSignature{} },
			want:   ter.TemMALFORMED,
		},
		{
			name:   "zero flags",
			tx:     newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) { sponsor(common, 0) },
			want:   ter.TemINVALID_FLAG,
		},
		{
			name:   "unknown flags",
			tx:     newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) { sponsor(common, 4) },
			want:   ter.TemINVALID_FLAG,
		},
		{
			name:   "self Sponsor",
			tx:     newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) { sponsor(common, txcore.SpfSponsorFee); common.Sponsor = common.Account },
			want:   ter.TemMALFORMED,
		},
		{
			name:   "fee sponsorship",
			tx:     newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) { sponsor(common, txcore.SpfSponsorFee) },
			want:   ter.TesSUCCESS,
		},
		{
			name: "reserve sponsorship allowed",
			tx: func() *txcore.BaseTx {
				tx := txcore.NewBaseTx(txcore.TypePayment, precedenceSourceAddr)
				tx.Fee = "10"
				tx.Sequence = u32(5)
				return tx
			}(),
			mutate: func(common *txcore.Common) { sponsor(common, txcore.SpfSponsorReserve) },
			want:   ter.TesSUCCESS,
		},
		{
			name: "reserve sponsorship rejected",
			tx:   txcore.NewBaseTx(txcore.TypeOfferCreate, precedenceSourceAddr),
			mutate: func(common *txcore.Common) {
				common.Fee = "10"
				common.Sequence = u32(5)
				sponsor(common, txcore.SpfSponsorReserve)
			},
			want: ter.TemINVALID_FLAG,
		},
		{
			name: "complete Sponsor signature definition",
			tx:   newAccountSet(precedenceSourceAddr),
			mutate: func(common *txcore.Common) {
				sponsor(common, txcore.SpfSponsorFee)
				common.SponsorSignature = &txcore.SponsorSignature{}
			},
			want: ter.TesSUCCESS,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.mutate(test.tx.GetCommon())
			if got := preflightEngine(sponsorRules).preflight(test.tx); got != test.want {
				t.Fatalf("preflight = %v, want %v", got, test.want)
			}
		})
	}
}

// --- test transaction types exercising the engine preflight seams ---

// flagMaskTx adopts FlagsMasker with a fixed invalid-flags mask.
type flagMaskTx struct {
	*txcore.BaseTx
	mask uint32
}

func (t *flagMaskTx) GetFlagsMask(*amendment.Rules) uint32 { return t.mask }

// extraFeaturesTx adopts ExtraFeaturesChecker with a fixed verdict.
type extraFeaturesTx struct {
	*txcore.BaseTx
	err error
}

func (t *extraFeaturesTx) CheckExtraFeatures(*amendment.Rules) error { return t.err }

// reqAmendmentTx overrides RequiredAmendments (the macro amendment gate).
type reqAmendmentTx struct {
	*txcore.BaseTx
	amendments [][32]byte
}

func (t *reqAmendmentTx) RequiredAmendments() [][32]byte { return t.amendments }

func newAccountSet(account string) *txcore.BaseTx {
	tx := txcore.NewBaseTx(txcore.TypeAccountSet, account)
	tx.Fee = "10"
	tx.Sequence = u32(5)
	return tx
}

// TestPreflightPrecedence_DelegateBeforeNetworkID pins finding E-delegate: the
// sfDelegate checks precede preflight0's NetworkID checks, so a delegate error
// wins over telNETWORK_ID_MAKES_TX_NON_CANONICAL.
func TestPreflightPrecedence_DelegateBeforeNetworkID(t *testing.T) {
	t.Run("delegate==account beats NetworkID", func(t *testing.T) {
		e := preflightEngine(batchRules("PermissionDelegationV1_1"))
		tx := newAccountSet(precedenceSourceAddr)
		tx.Delegate = precedenceSourceAddr // == Account → temBAD_SIGNER
		tx.NetworkID = u32(99)             // legacy node (ID 0) forbids the field
		if got := e.preflight(tx); got != ter.TemBAD_SIGNER {
			t.Fatalf("preflight = %v, want TemBAD_SIGNER", got)
		}
	})

	t.Run("delegate-disabled beats NetworkID", func(t *testing.T) {
		e := preflightEngine(allRules()) // PermissionDelegationV1_1 is Supported::no → disabled
		tx := newAccountSet(precedenceSourceAddr)
		tx.Delegate = precedenceGenesisAddr // present, PermissionDelegationV1_1 off → temDISABLED
		tx.NetworkID = u32(99)
		if got := e.preflight(tx); got != ter.TemDISABLED {
			t.Fatalf("preflight = %v, want TemDISABLED", got)
		}
	})
}

// TestPreflightPrecedence_NetworkIDBeforeAccount pins finding E-account: the
// NetworkID checks (preflight0) run before the zero-account check (preflight1),
// so a NetworkID violation wins over temBAD_SRC_ACCOUNT.
func TestPreflightPrecedence_NetworkIDBeforeAccount(t *testing.T) {
	e := preflightEngine(allRules())
	tx := txcore.NewBaseTx(txcore.TypeAccountSet, "") // empty account
	tx.Fee = "10"
	tx.Sequence = u32(5)
	tx.NetworkID = u32(99) // legacy node forbids the field → tel*
	if got := e.preflight(tx); got != ter.TelNETWORK_ID_MAKES_TX_NON_CANONICAL {
		t.Fatalf("preflight = %v, want TelNETWORK_ID_MAKES_TX_NON_CANONICAL", got)
	}
}

// TestPreflightPrecedence_AccountBeforeFee pins that the zero-account check
// precedes the fee check (rippled preflight1 order account → fee).
func TestPreflightPrecedence_AccountBeforeFee(t *testing.T) {
	e := preflightEngine(allRules())
	tx := txcore.NewBaseTx(txcore.TypeAccountSet, "") // empty account
	tx.Fee = "-10"                                    // malformed fee, not reached
	tx.Sequence = u32(5)
	if got := e.preflight(tx); got != ter.TemBAD_SRC_ACCOUNT {
		t.Fatalf("preflight = %v, want TemBAD_SRC_ACCOUNT", got)
	}
}

// TestPreflightPrecedence_FeeBeforeSigningKey pins finding E-fee: the fee check
// precedes the signing-key shape check, so a malformed fee wins over
// temBAD_SIGNATURE.
func TestPreflightPrecedence_FeeBeforeSigningKey(t *testing.T) {
	e := preflightEngine(allRules())
	tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
	tx.Fee = "-10" // malformed fee → temBAD_FEE
	tx.Sequence = u32(5)
	tx.SigningPubKey = "00" // invalid key type → temBAD_SIGNATURE if reached
	if got := e.preflight(tx); got != ter.TemBAD_FEE {
		t.Fatalf("preflight = %v, want TemBAD_FEE", got)
	}
}

// TestPreflightPrecedence_TicketAccountTxnID pins finding E-seqproxy: the
// ticket+AccountTxnID temINVALID fires only when getSeqProxy().isTicket(), i.e.
// Sequence is zero/absent. A tx with a real Sequence ignores its TicketSequence.
func TestPreflightPrecedence_TicketAccountTxnID(t *testing.T) {
	const txnID = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	e := preflightEngine(allRules())

	t.Run("nonzero Sequence with TicketSequence+AccountTxnID passes", func(t *testing.T) {
		tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		tx.Fee = "10"
		tx.Sequence = u32(5) // real sequence → ticket ignored → AccountTxnID allowed
		tx.TicketSequence = u32(7)
		tx.AccountTxnID = txnID
		if got := e.preflight(tx); got != ter.TesSUCCESS {
			t.Fatalf("preflight = %v, want TesSUCCESS", got)
		}
	})

	t.Run("zero Sequence with TicketSequence+AccountTxnID is temINVALID", func(t *testing.T) {
		tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		tx.Fee = "10"
		tx.Sequence = u32(0) // zero → getSeqProxy().isTicket() true
		tx.TicketSequence = u32(7)
		tx.AccountTxnID = txnID
		if got := e.preflight(tx); got != ter.TemINVALID {
			t.Fatalf("preflight = %v, want TemINVALID", got)
		}
	})

	t.Run("ticket-only with AccountTxnID is temINVALID", func(t *testing.T) {
		tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		tx.Fee = "10"
		tx.TicketSequence = u32(7) // Sequence absent → isTicket() true
		tx.AccountTxnID = txnID
		if got := e.preflight(tx); got != ter.TemINVALID {
			t.Fatalf("preflight = %v, want TemINVALID", got)
		}
	})
}

// TestPreflightPrecedence_InnerBatchFlagLast pins finding E-innerbatch: the
// outer tfInnerBatchTxn rejection is the last preflight1 check, so a malformed
// fee (Batch disabled) wins over temINVALID_FLAG.
func TestPreflightPrecedence_InnerBatchFlagLast(t *testing.T) {
	e := preflightEngine(allRules()) // Batch is Supported::no → disabled
	tx := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
	tx.Fee = "-10" // malformed fee → temBAD_FEE
	tx.Sequence = u32(5)
	flags := txcore.TfInnerBatchTxn
	tx.Flags = &flags
	if got := e.preflight(tx); got != ter.TemBAD_FEE {
		t.Fatalf("preflight = %v, want TemBAD_FEE", got)
	}
}

// TestPreflightPrecedence_MacroGateBeforeNetworkID pins finding E1: the
// transactions.macro amendment gate (RequiredAmendments) is the first
// invokePreflight check, ahead of preflight0's NetworkID checks.
func TestPreflightPrecedence_MacroGateBeforeNetworkID(t *testing.T) {
	e := preflightEngine(allRules()) // Batch disabled
	tx := &reqAmendmentTx{
		BaseTx:     newAccountSet(precedenceSourceAddr),
		amendments: [][32]byte{amendment.FeatureBatch},
	}
	tx.NetworkID = u32(99) // legacy node forbids the field → tel* if reached
	if got := e.preflight(tx); got != ter.TemDISABLED {
		t.Fatalf("preflight = %v, want TemDISABLED", got)
	}
}

// TestPreflightMechanism_FlagsMasker pins the FlagsMasker seam: a type that
// adopts it is rejected temINVALID_FLAG when its flags intersect the mask, and
// only then; a type that does not adopt it gets no engine-level flag rejection.
func TestPreflightMechanism_FlagsMasker(t *testing.T) {
	e := preflightEngine(allRules())

	t.Run("flag in mask is rejected", func(t *testing.T) {
		bit := uint32(0x00010000)
		base := newAccountSet(precedenceSourceAddr)
		base.Flags = &bit
		tx := &flagMaskTx{BaseTx: base, mask: bit}
		if got := e.preflight(tx); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
		}
	})

	t.Run("flag outside mask passes", func(t *testing.T) {
		other := uint32(0x00020000)
		base := newAccountSet(precedenceSourceAddr)
		base.Flags = &other
		tx := &flagMaskTx{BaseTx: base, mask: 0x00010000}
		if got := e.preflight(tx); got != ter.TesSUCCESS {
			t.Fatalf("preflight = %v, want TesSUCCESS", got)
		}
	})

	t.Run("type without FlagsMasker gets no engine flag check", func(t *testing.T) {
		bit := uint32(0x00010000) // a non-universal flag the engine must not reject on its own
		base := newAccountSet(precedenceSourceAddr)
		base.Flags = &bit
		if got := e.preflight(base); got != ter.TesSUCCESS {
			t.Fatalf("preflight = %v, want TesSUCCESS", got)
		}
	})
}

// TestPreflight_AcceptsOversizedMemo pins the memo relocation: the engine
// preflight no longer validates memos, so an oversized-memo transaction that
// would be refused at local submission (PassesLocalChecks) still passes
// preflight and can be applied. This is what prevents a memo-violating tx in an
// agreed tx set from forking the ledger.
func TestPreflight_AcceptsOversizedMemo(t *testing.T) {
	e := preflightEngine(allRules())
	tx := newAccountSet(precedenceSourceAddr)
	tx.Memos = []txcore.MemoWrapper{{Memo: txcore.Memo{MemoData: strings.Repeat("AA", 1100)}}}
	if got := e.preflight(tx); got != ter.TesSUCCESS {
		t.Fatalf("preflight(oversized memo) = %v, want TesSUCCESS", got)
	}
}

// TestPreflightMechanism_ExtraFeatures pins the ExtraFeaturesChecker seam: it
// runs before the common checks, so a temDISABLED verdict wins over a malformed
// fee; a nil verdict does not disturb an otherwise-clean transaction.
func TestPreflightMechanism_ExtraFeatures(t *testing.T) {
	e := preflightEngine(allRules())

	t.Run("extra-features verdict beats bad fee", func(t *testing.T) {
		base := newAccountSet(precedenceSourceAddr)
		base.Fee = "-10" // malformed fee, not reached
		tx := &extraFeaturesTx{BaseTx: base, err: ter.Errorf(ter.TemDISABLED, "feature disabled")}
		if got := e.preflight(tx); got != ter.TemDISABLED {
			t.Fatalf("preflight = %v, want TemDISABLED", got)
		}
	})

	t.Run("nil verdict passes", func(t *testing.T) {
		tx := &extraFeaturesTx{BaseTx: newAccountSet(precedenceSourceAddr), err: nil}
		if got := e.preflight(tx); got != ter.TesSUCCESS {
			t.Fatalf("preflight = %v, want TesSUCCESS", got)
		}
	})
}

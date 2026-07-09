package vault

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// mptArmsView is a minimal in-memory tx.LedgerView for the MPT-arm helper tests.
type mptArmsView struct{ data map[[32]byte][]byte }

func newMPTArmsView() *mptArmsView { return &mptArmsView{data: make(map[[32]byte][]byte)} }

func (m *mptArmsView) Read(k keylet.Keylet) ([]byte, error)      { return m.data[k.Key], nil }
func (m *mptArmsView) Exists(k keylet.Keylet) (bool, error)      { _, ok := m.data[k.Key]; return ok, nil }
func (m *mptArmsView) Insert(k keylet.Keylet, data []byte) error { m.data[k.Key] = data; return nil }
func (m *mptArmsView) Update(k keylet.Keylet, data []byte) error { m.data[k.Key] = data; return nil }
func (m *mptArmsView) Erase(k keylet.Keylet) error               { delete(m.data, k.Key); return nil }
func (m *mptArmsView) AdjustDropsDestroyed(drops.XRPAmount)      {}
func (m *mptArmsView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
	return nil
}
func (m *mptArmsView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (m *mptArmsView) TxExists([32]byte) bool  { return false }
func (m *mptArmsView) Rules() *amendment.Rules { return nil }
func (m *mptArmsView) LedgerSeq() uint32       { return 0 }

func rulesWithFix(on bool) *amendment.Rules {
	b := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	if on {
		b = b.Enable(amendment.FeatureID("fixCleanup3_1_3"))
	} else {
		b = b.Disable(amendment.FeatureID("fixCleanup3_1_3"))
	}
	return b.Build()
}

func buildArmsCtx(t *testing.T, view *mptArmsView, holderID [20]byte, rules *amendment.Rules) *tx.ApplyContext {
	t.Helper()
	holderAddr, err := state.EncodeAccountID(holderID)
	if err != nil {
		t.Fatalf("encode holder: %v", err)
	}
	acct := &state.AccountRoot{Account: holderAddr, Balance: 100_000_000, Sequence: 1, OwnerCount: 1}
	blob, serr := state.SerializeAccountRoot(acct)
	if serr != nil {
		t.Fatalf("serialize account: %v", serr)
	}
	if ierr := view.Insert(keylet.Account(holderID), blob); ierr != nil {
		t.Fatalf("insert account: %v", ierr)
	}
	return &tx.ApplyContext{
		View:      view,
		Account:   acct,
		AccountID: holderID,
		Config:    tx.EngineConfig{Rules: rules},
		Metadata:  &tx.Metadata{},
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}
}

// TestRemoveEmptyShareMPToken_LockedGate covers the fixCleanup3_1_3-gated arm:
// a zero-balance share holding with a non-zero LockedAmount can be deleted with
// the amendment off, but is refused (tecHAS_OBLIGATIONS) with it on.
func TestRemoveEmptyShareMPToken_LockedGate(t *testing.T) {
	var holderID [20]byte
	for i := range holderID {
		holderID[i] = 0x33
	}
	var shareMPTID [24]byte
	for i := range shareMPTID {
		shareMPTID[i] = 0x44
	}

	build := func(t *testing.T, rules *amendment.Rules) (*tx.ApplyContext, *mptArmsView) {
		view := newMPTArmsView()
		ctx := buildArmsCtx(t, view, holderID, rules)

		tokenKey := keylet.MPTokenByID(shareMPTID, holderID)
		dirRes, derr := state.DirInsert(view, keylet.OwnerDir(holderID), tokenKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = holderID
		})
		if derr != nil {
			t.Fatalf("dir insert: %v", derr)
		}
		locked := uint64(5)
		token := &state.MPTokenData{
			Account:           holderID,
			MPTokenIssuanceID: shareMPTID,
			MPTAmount:         0,
			LockedAmount:      &locked,
			OwnerNode:         dirRes.Page,
		}
		blob, serr := state.SerializeMPToken(token)
		if serr != nil {
			t.Fatalf("serialize token: %v", serr)
		}
		if ierr := view.Insert(tokenKey, blob); ierr != nil {
			t.Fatalf("insert token: %v", ierr)
		}
		return ctx, view
	}

	t.Run("fix on: locked holding cannot be deleted", func(t *testing.T) {
		ctx, view := build(t, rulesWithFix(true))
		if res := removeEmptyShareMPToken(ctx, holderID, shareMPTID); res != ter.TecHAS_OBLIGATIONS {
			t.Fatalf("got %v, want tecHAS_OBLIGATIONS", res)
		}
		if _, ok := view.data[keylet.MPTokenByID(shareMPTID, holderID).Key]; !ok {
			t.Fatal("locked holding must not be erased")
		}
	})

	t.Run("fix off: locked holding is deleted", func(t *testing.T) {
		ctx, view := build(t, rulesWithFix(false))
		if res := removeEmptyShareMPToken(ctx, holderID, shareMPTID); res != ter.TesSUCCESS {
			t.Fatalf("got %v, want tesSUCCESS", res)
		}
		if _, ok := view.data[keylet.MPTokenByID(shareMPTID, holderID).Key]; ok {
			t.Fatal("holding should be erased when the amendment is off")
		}
	})
}

// TestSendMPTAsset_MaximumAmountCap covers the issuer-as-sender MaximumAmount cap.
// The cap mirrors rippled's unconditional single-send rippleSendMPT check, so it
// fires in both amendment states; the fixCleanup3_1_3 gate only refines the
// multi-destination aggregate, which go-xrpl reaches via committed per-leg sends.
func TestSendMPTAsset_MaximumAmountCap(t *testing.T) {
	var issuerID [20]byte
	for i := range issuerID {
		issuerID[i] = 0x55
	}
	// MakeMPTID packs the issuer into bytes 4..24, so mptIDIssuer(mptID)==issuerID.
	mptID := keylet.MakeMPTID(7, issuerID)
	var holderID [20]byte
	for i := range holderID {
		holderID[i] = 0x66
	}

	build := func(t *testing.T, rules *amendment.Rules, maxAmount, outstanding uint64) *tx.ApplyContext {
		view := newMPTArmsView()
		ctx := buildArmsCtx(t, view, issuerID, rules)
		maxCopy := maxAmount
		iss := &state.MPTokenIssuanceData{
			Issuer:            issuerID,
			Sequence:          7,
			OutstandingAmount: outstanding,
			MaximumAmount:     &maxCopy,
		}
		blob, serr := state.SerializeMPTokenIssuance(iss)
		if serr != nil {
			t.Fatalf("serialize issuance: %v", serr)
		}
		if ierr := view.Insert(keylet.MPTIssuance(mptID), blob); ierr != nil {
			t.Fatalf("insert issuance: %v", ierr)
		}
		return ctx
	}

	for _, on := range []bool{true, false} {
		name := "fix off"
		if on {
			name = "fix on"
		}
		t.Run(name, func(t *testing.T) {
			// This send alone exceeds the cap.
			ctx := build(t, rulesWithFix(on), 1000, 0)
			if res := sendMPTAsset(ctx, mptID, issuerID, holderID, 2000); res != ter.TecPATH_DRY {
				t.Fatalf("single send over max: got %v, want tecPATH_DRY", res)
			}

			// Outstanding + this send exceeds the cap (the aggregate arm).
			ctx = build(t, rulesWithFix(on), 1000, 600)
			if res := sendMPTAsset(ctx, mptID, issuerID, holderID, 500); res != ter.TecPATH_DRY {
				t.Fatalf("aggregate over max: got %v, want tecPATH_DRY", res)
			}
		})
	}
}

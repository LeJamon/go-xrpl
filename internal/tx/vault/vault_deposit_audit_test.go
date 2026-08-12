package vault

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type vaultDepositView struct {
	data  map[[32]byte][]byte
	rules *amendment.Rules
}

func newVaultDepositView() *vaultDepositView {
	return &vaultDepositView{
		data: make(map[[32]byte][]byte),
		rules: amendment.NewRulesBuilder().
			FromPreset(amendment.PresetAllSupported).
			Build(),
	}
}

func (v *vaultDepositView) Read(k keylet.Keylet) ([]byte, error) { return v.data[k.Key], nil }
func (v *vaultDepositView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}
func (v *vaultDepositView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *vaultDepositView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *vaultDepositView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}
func (v *vaultDepositView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (v *vaultDepositView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for key, data := range v.data {
		if !fn(key, data) {
			break
		}
	}
	return nil
}
func (v *vaultDepositView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *vaultDepositView) TxExists([32]byte) (bool, error) { return false, nil }
func (v *vaultDepositView) Rules() *amendment.Rules         { return v.rules }
func (v *vaultDepositView) LedgerSeq() uint32               { return 1 }
func vaultDepositTestID(fill byte, size int) []byte         { return makeFilledBytes(fill, size) }
func makeFilledBytes(fill byte, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = fill
	}
	return b
}

func vaultDepositPut(t *testing.T, view *vaultDepositView, key keylet.Keylet, data []byte) {
	t.Helper()
	view.data[key.Key] = data
}

func vaultDepositEncoded(t *testing.T, data []byte, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func vaultDepositAccount(t *testing.T, view *vaultDepositView, id [20]byte, flags uint32) string {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: address,
		Balance: 1_000_000_000,
		Flags:   flags,
	})
	vaultDepositPut(t, view, keylet.Account(id), vaultDepositEncoded(t, data, err))
	return address
}

func vaultDepositFixture(t *testing.T, issuanceFlags uint32, withShareIssuance bool) (*vaultDepositView, *VaultDeposit, [24]byte, [20]byte) {
	t.Helper()
	view := newVaultDepositView()
	var issuer, depositor, owner, pseudo [20]byte
	copy(issuer[:], vaultDepositTestID(0x11, 20))
	copy(depositor[:], vaultDepositTestID(0x22, 20))
	copy(owner[:], vaultDepositTestID(0x33, 20))
	copy(pseudo[:], vaultDepositTestID(0x44, 20))
	issuerAddress := vaultDepositAccount(t, view, issuer, 0)
	depositorAddress := vaultDepositAccount(t, view, depositor, 0)
	mptID := keylet.MakeMPTID(7, issuer)
	shareID := keylet.MakeMPTID(1, pseudo)
	issuanceData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          7,
		Flags:             issuanceFlags,
		OutstandingAmount: 1_000,
	})
	vaultDepositPut(t, view, keylet.MPTIssuance(mptID), vaultDepositEncoded(t, issuanceData, err))
	if withShareIssuance {
		shareData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer:   pseudo,
			Sequence: 1,
		})
		vaultDepositPut(t, view, keylet.MPTIssuance(shareID), vaultDepositEncoded(t, shareData, err))
	}
	var vaultID [32]byte
	copy(vaultID[:], vaultDepositTestID(0x55, 32))
	vaultData, err := serializeVault(&vaultData{
		Owner:            owner,
		Account:          pseudo,
		AssetIsMPT:       true,
		AssetMPTID:       mptID,
		ShareMPTID:       shareID,
		AssetsTotal:      "1000",
		AssetsAvailable:  "1000",
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
	})
	vaultDepositPut(t, view, keylet.VaultByID(vaultID), vaultDepositEncoded(t, vaultData, err))
	deposit := NewVaultDeposit(
		depositorAddress,
		hex.EncodeToString(vaultID[:]),
		state.NewMPTAmountWithIssuanceID(100, issuerAddress, hex.EncodeToString(mptID[:])),
	)
	return view, deposit, mptID, depositor
}

func TestVaultDepositPreclaim_CanTransferPrecedesShareLookup(t *testing.T) {
	view, deposit, _, _ := vaultDepositFixture(t, entry.LsfMPTRequireAuth, false)
	config := tx.EngineConfig{Rules: view.rules}
	if result := deposit.Preclaim(view, config); result != ter.TecNO_AUTH {
		t.Fatalf("non-transferable MPT: got %v, want tecNO_AUTH", result)
	}

	view, deposit, _, _ = vaultDepositFixture(t, entry.LsfMPTCanTransfer|entry.LsfMPTRequireAuth, false)
	if result := deposit.Preclaim(view, config); result != ter.TefINTERNAL {
		t.Fatalf("transferable MPT with missing share issuance: got %v, want tefINTERNAL", result)
	}
}

func TestVaultDepositPreclaim_DispatchesMPTRequireAuth(t *testing.T) {
	view, deposit, mptID, depositor := vaultDepositFixture(
		t,
		entry.LsfMPTCanTransfer|entry.LsfMPTRequireAuth,
		true,
	)
	putToken := func(flags uint32) {
		t.Helper()
		data, err := state.SerializeMPToken(&state.MPTokenData{
			Account:           depositor,
			MPTokenIssuanceID: mptID,
			MPTAmount:         1_000,
			Flags:             flags,
		})
		vaultDepositPut(t, view, keylet.MPTokenByID(mptID, depositor), vaultDepositEncoded(t, data, err))
	}
	putToken(0)
	config := tx.EngineConfig{Rules: view.rules}
	if result := deposit.Preclaim(view, config); result != ter.TecNO_AUTH {
		t.Fatalf("unauthorized MPT holding: got %v, want tecNO_AUTH", result)
	}
	putToken(entry.LsfMPTAuthorized)
	if result := deposit.Preclaim(view, config); result != ter.TesSUCCESS {
		t.Fatalf("authorized MPT holding: got %v, want tesSUCCESS", result)
	}
}

func TestVaultDepositPreclaim_FixCleanup330ChecksPseudoMPTFreeze(t *testing.T) {
	view, deposit, mptID, depositor := vaultDepositFixture(t, entry.LsfMPTCanTransfer, true)
	var pseudo [20]byte
	copy(pseudo[:], vaultDepositTestID(0x44, 20))

	putToken := func(account [20]byte, flags uint32) {
		t.Helper()
		data, err := state.SerializeMPToken(&state.MPTokenData{
			Account:           account,
			MPTokenIssuanceID: mptID,
			MPTAmount:         1_000,
			Flags:             flags,
		})
		vaultDepositPut(t, view, keylet.MPTokenByID(mptID, account), vaultDepositEncoded(t, data, err))
	}
	putToken(depositor, 0)
	putToken(pseudo, entry.LsfMPTLocked)

	fixOff := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureFixCleanup3_3_0).
		Build()
	view.rules = fixOff
	if result := deposit.Preclaim(view, tx.EngineConfig{Rules: fixOff}); result != ter.TesSUCCESS {
		t.Fatalf("fixCleanup3_3_0 OFF: got %v, want tesSUCCESS", result)
	}

	fixOn := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureFixCleanup3_3_0).
		Build()
	view.rules = fixOn
	if result := deposit.Preclaim(view, tx.EngineConfig{Rules: fixOn}); result != ter.TecLOCKED {
		t.Fatalf("fixCleanup3_3_0 ON: got %v, want tecLOCKED", result)
	}
}

func TestVaultDepositPreclaim_IOUUsesFullBalance(t *testing.T) {
	view := newVaultDepositView()
	var issuer, depositor, owner, pseudo [20]byte
	copy(issuer[:], vaultDepositTestID(0x11, 20))
	copy(depositor[:], vaultDepositTestID(0x22, 20))
	copy(owner[:], vaultDepositTestID(0x33, 20))
	copy(pseudo[:], vaultDepositTestID(0x44, 20))
	issuerAddress := vaultDepositAccount(t, view, issuer, state.LsfDefaultRipple)
	depositorAddress := vaultDepositAccount(t, view, depositor, 0)
	pseudoAddress := vaultDepositAccount(t, view, pseudo, 0)

	line := func(account [20]byte, accountAddress string, balance, oppositeLimit int64) []byte {
		data, err := state.SerializeRippleState(&state.RippleState{
			Balance:   state.NewIssuedAmountFromValue(balance, 0, "USD", issuerAddress),
			LowLimit:  state.NewIssuedAmountFromValue(oppositeLimit, 0, "USD", issuerAddress),
			HighLimit: state.NewIssuedAmountFromValue(1_000, 0, "USD", accountAddress),
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	view.data[keylet.Line(depositor, issuer, "USD").Key] = line(depositor, depositorAddress, -100, 1_000)
	view.data[keylet.Line(pseudo, issuer, "USD").Key] = line(pseudo, pseudoAddress, 0, 0)

	shareID := keylet.MakeMPTID(1, pseudo)
	shareData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer: pseudo,
	})
	vaultDepositPut(t, view, keylet.MPTIssuance(shareID), vaultDepositEncoded(t, shareData, err))
	var vaultID [32]byte
	copy(vaultID[:], vaultDepositTestID(0x66, 32))
	vaultData, err := serializeVault(&vaultData{
		Owner:            owner,
		Account:          pseudo,
		Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddress},
		ShareMPTID:       shareID,
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
	})
	vaultDepositPut(t, view, keylet.VaultByID(vaultID), vaultDepositEncoded(t, vaultData, err))
	deposit := NewVaultDeposit(
		depositorAddress,
		hex.EncodeToString(vaultID[:]),
		state.NewIssuedAmountFromValue(500, 0, "USD", issuerAddress),
	)
	config := tx.EngineConfig{Rules: view.rules}
	if result := deposit.Preclaim(view, config); result != ter.TesSUCCESS {
		t.Fatalf("raw 100 + opposite limit 1000: got %v, want tesSUCCESS", result)
	}

	view.data[keylet.Line(depositor, issuer, "USD").Key] = line(depositor, depositorAddress, -100, 0)
	if result := deposit.Preclaim(view, config); result != ter.TecINSUFFICIENT_FUNDS {
		t.Fatalf("raw balance 100 without opposite limit: got %v, want tecINSUFFICIENT_FUNDS", result)
	}
}

func TestVaultDepositPreFixRejectsNegativeDepositorBalance(t *testing.T) {
	view := newVaultDepositView()
	view.rules = amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureFixCleanup3_2_0).
		Build()
	var issuer, depositor, owner, pseudo [20]byte
	copy(issuer[:], vaultDepositTestID(0x11, 20))
	copy(depositor[:], vaultDepositTestID(0x22, 20))
	copy(owner[:], vaultDepositTestID(0x33, 20))
	copy(pseudo[:], vaultDepositTestID(0x44, 20))
	issuerAddress := vaultDepositAccount(t, view, issuer, state.LsfDefaultRipple)
	depositorAddress := vaultDepositAccount(t, view, depositor, 0)
	pseudoAddress := vaultDepositAccount(t, view, pseudo, 0)

	line := func(accountAddress string, balance, oppositeLimit int64) []byte {
		data, err := state.SerializeRippleState(&state.RippleState{
			Balance:   state.NewIssuedAmountFromValue(balance, 0, "USD", issuerAddress),
			LowLimit:  state.NewIssuedAmountFromValue(oppositeLimit, 0, "USD", issuerAddress),
			HighLimit: state.NewIssuedAmountFromValue(1_000, 0, "USD", accountAddress),
		})
		return vaultDepositEncoded(t, data, err)
	}
	view.data[keylet.Line(depositor, issuer, "USD").Key] = line(depositorAddress, -100, 1_000)
	view.data[keylet.Line(pseudo, issuer, "USD").Key] = line(pseudoAddress, 0, 0)

	shareID := keylet.MakeMPTID(1, pseudo)
	shareData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{Issuer: pseudo, Sequence: 1})
	vaultDepositPut(t, view, keylet.MPTIssuance(shareID), vaultDepositEncoded(t, shareData, err))
	tokenData, err := state.SerializeMPToken(&state.MPTokenData{Account: depositor, MPTokenIssuanceID: shareID})
	vaultDepositPut(t, view, keylet.MPTokenByID(shareID, depositor), vaultDepositEncoded(t, tokenData, err))

	var vaultID [32]byte
	copy(vaultID[:], vaultDepositTestID(0x77, 32))
	vaultData, err := serializeVaultForRules(&vaultData{
		Owner:            owner,
		Account:          pseudo,
		Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddress},
		ShareMPTID:       shareID,
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
	}, view.rules)
	vaultDepositPut(t, view, keylet.VaultByID(vaultID), vaultDepositEncoded(t, vaultData, err))

	account := &state.AccountRoot{Account: depositorAddress, Balance: 1_000_000_000}
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: depositor,
		Config:    tx.EngineConfig{Rules: view.rules},
		Metadata:  &tx.Metadata{},
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}
	deposit := NewVaultDeposit(
		depositorAddress,
		hex.EncodeToString(vaultID[:]),
		state.NewIssuedAmountFromValue(500, 0, "USD", issuerAddress),
	)
	if got := deposit.Apply(ctx); got != ter.TefINTERNAL {
		t.Fatalf("Apply() = %v, want tefINTERNAL", got)
	}
}

func TestVaultDepositExchange_OverflowReturnsPathDry(t *testing.T) {
	zero := state.NewXRPLNumber(0, 0)
	assets := state.NewXRPLNumber(10, 0)
	_, _, _, result := vaultDepositExchange(zero, zero, assets, 18, true)
	if result != ter.TecPATH_DRY {
		t.Fatalf("scale 18 first deposit: got %v, want tecPATH_DRY", result)
	}
	_, _, shares, result := vaultDepositExchange(zero, zero, assets, 1, true)
	if result != ter.TesSUCCESS || shares != 100 {
		t.Fatalf("scale 1 control: got (%d, %v), want (100, tesSUCCESS)", shares, result)
	}
}

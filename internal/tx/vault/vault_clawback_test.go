package vault

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type vaultClawbackFixture struct {
	view          *mptArmsView
	ctx           *tx.ApplyContext
	txn           *VaultClawback
	vaultKey      keylet.Keylet
	assetIssuerID [20]byte
	ownerID       [20]byte
	holderID      [20]byte
	pseudoID      [20]byte
	assetMPTID    [24]byte
	shareMPTID    [24]byte
}

func newVaultClawbackFixture(t *testing.T, holderShares uint64) *vaultClawbackFixture {
	t.Helper()
	assetIssuerID := clawbackTestID(0x51)
	ownerID := clawbackTestID(0x52)
	holderID := clawbackTestID(0x53)
	pseudoID := clawbackTestID(0x54)
	assetMPTID := keylet.MakeMPTID(11, assetIssuerID)
	shareMPTID := keylet.MakeMPTID(12, pseudoID)
	var vaultID [32]byte
	for i := range vaultID {
		vaultID[i] = 0x55
	}
	vaultKey := keylet.VaultByID(vaultID)
	view := newMPTArmsView()

	vaultBytes, err := serializeVault(&vaultData{
		Owner:            ownerID,
		Account:          pseudoID,
		Sequence:         1,
		ShareMPTID:       shareMPTID,
		AssetIsMPT:       true,
		AssetMPTID:       assetMPTID,
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
		AssetsTotal:      "100",
		AssetsAvailable:  "100",
	})
	if err != nil {
		t.Fatalf("serialize vault: %v", err)
	}
	view.data[vaultKey.Key] = vaultBytes

	assetIssuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer: assetIssuerID, Sequence: 11, OutstandingAmount: 100, Flags: entry.LsfMPTCanClawback,
	})
	if err != nil {
		t.Fatalf("serialize asset issuance: %v", err)
	}
	view.data[keylet.MPTIssuance(assetMPTID).Key] = assetIssuance
	assetToken, err := state.SerializeMPToken(&state.MPTokenData{
		Account: pseudoID, MPTokenIssuanceID: assetMPTID, MPTAmount: 100,
	})
	if err != nil {
		t.Fatalf("serialize vault asset token: %v", err)
	}
	view.data[keylet.MPTokenByID(assetMPTID, pseudoID).Key] = assetToken

	shareIssuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer: pseudoID, Sequence: 12, OutstandingAmount: 100,
	})
	if err != nil {
		t.Fatalf("serialize share issuance: %v", err)
	}
	view.data[keylet.MPTIssuance(shareMPTID).Key] = shareIssuance
	shareToken, err := state.SerializeMPToken(&state.MPTokenData{
		Account: holderID, MPTokenIssuanceID: shareMPTID, MPTAmount: holderShares,
	})
	if err != nil {
		t.Fatalf("serialize holder share token: %v", err)
	}
	view.data[keylet.MPTokenByID(shareMPTID, holderID).Key] = shareToken

	issuerAddr := clawbackTestAddress(t, assetIssuerID)
	holderAddr := clawbackTestAddress(t, holderID)
	issuerAccount := &state.AccountRoot{Account: issuerAddr, Balance: 1_000_000_000, Sequence: 1}
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   issuerAccount,
		AccountID: assetIssuerID,
		Config:    tx.EngineConfig{Rules: rulesWithFix(true)},
		Metadata:  &tx.Metadata{},
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}
	txn := NewVaultClawback(issuerAddr, hex.EncodeToString(vaultID[:]), holderAddr)
	return &vaultClawbackFixture{
		view:          view,
		ctx:           ctx,
		txn:           txn,
		vaultKey:      vaultKey,
		assetIssuerID: assetIssuerID,
		ownerID:       ownerID,
		holderID:      holderID,
		pseudoID:      pseudoID,
		assetMPTID:    assetMPTID,
		shareMPTID:    shareMPTID,
	}
}

func TestVaultClawbackMPTPreclaimAuthorizationAndPrecedence(t *testing.T) {
	newCase := func(t *testing.T) (*vaultClawbackFixture, tx.Amount) {
		t.Helper()
		f := newVaultClawbackFixture(t, 100)
		amount := state.NewMPTAmountWithIssuanceID(
			0,
			clawbackTestAddress(t, f.assetIssuerID),
			hex.EncodeToString(f.assetMPTID[:]),
		)
		f.txn.Amount = &amount
		delete(f.view.data, keylet.MPTIssuance(f.assetMPTID).Key)
		return f, amount
	}

	t.Run("issuer check precedes missing issuance", func(t *testing.T) {
		f, _ := newCase(t)
		f.txn.Account = clawbackTestAddress(t, clawbackTestID(0x61))
		if got := f.txn.Preclaim(f.view, f.ctx.Config); got != ter.TecNO_PERMISSION {
			t.Fatalf("Preclaim() = %v, want tecNO_PERMISSION", got)
		}
	})

	t.Run("missing issuance", func(t *testing.T) {
		f, _ := newCase(t)
		if got := f.txn.Preclaim(f.view, f.ctx.Config); got != ter.TecOBJECT_NOT_FOUND {
			t.Fatalf("Preclaim() = %v, want tecOBJECT_NOT_FOUND", got)
		}
	})

	t.Run("clawback disabled", func(t *testing.T) {
		f, _ := newCase(t)
		data, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer: f.assetIssuerID, Sequence: 11,
		})
		if err != nil {
			t.Fatalf("serialize asset issuance: %v", err)
		}
		f.view.data[keylet.MPTIssuance(f.assetMPTID).Key] = data
		if got := f.txn.Preclaim(f.view, f.ctx.Config); got != ter.TecNO_PERMISSION {
			t.Fatalf("Preclaim() = %v, want tecNO_PERMISSION", got)
		}
	})

	t.Run("clawback enabled", func(t *testing.T) {
		f, _ := newCase(t)
		data, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer: f.assetIssuerID, Sequence: 11, Flags: entry.LsfMPTCanClawback,
		})
		if err != nil {
			t.Fatalf("serialize asset issuance: %v", err)
		}
		f.view.data[keylet.MPTIssuance(f.assetMPTID).Key] = data
		if got := f.txn.Preclaim(f.view, f.ctx.Config); got != ter.TesSUCCESS {
			t.Fatalf("Preclaim() = %v, want tesSUCCESS", got)
		}
	})
}

func TestVaultClawbackMPTImplicitOwnerIssuerIsAmbiguous(t *testing.T) {
	f := newVaultClawbackFixture(t, 100)
	vaultBytes, err := serializeVault(&vaultData{
		Owner:            f.assetIssuerID,
		Account:          f.pseudoID,
		Sequence:         1,
		ShareMPTID:       f.shareMPTID,
		AssetIsMPT:       true,
		AssetMPTID:       f.assetMPTID,
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
		AssetsTotal:      "100",
		AssetsAvailable:  "100",
	})
	if err != nil {
		t.Fatalf("serialize vault: %v", err)
	}
	f.view.data[f.vaultKey.Key] = vaultBytes
	if got := f.txn.Preclaim(f.view, f.ctx.Config); got != ter.TecWRONG_ASSET {
		t.Fatalf("Preclaim() = %v, want tecWRONG_ASSET", got)
	}
}

func TestVaultClawbackTransfersMPTAssetToIssuer(t *testing.T) {
	f := newVaultClawbackFixture(t, 100)
	amount := state.NewMPTAmountWithIssuanceID(
		40,
		clawbackTestAddress(t, f.assetIssuerID),
		hex.EncodeToString(f.assetMPTID[:]),
	)
	f.txn.Amount = &amount
	if got := f.txn.Apply(f.ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply() = %v, want tesSUCCESS", got)
	}

	vaultBytes := f.view.data[f.vaultKey.Key]
	updatedVault, err := parseVault(vaultBytes)
	if err != nil {
		t.Fatalf("parse updated vault: %v", err)
	}
	total, err := vaultNumber(updatedVault.AssetsTotal)
	if err != nil {
		t.Fatalf("parse AssetsTotal: %v", err)
	}
	available, err := vaultNumber(updatedVault.AssetsAvailable)
	if err != nil {
		t.Fatalf("parse AssetsAvailable: %v", err)
	}
	want := state.NewXRPLNumber(60, 0)
	if total.Cmp(want) != 0 || available.Cmp(want) != 0 {
		t.Fatalf("vault totals = (%s, %s), want (60, 60)", updatedVault.AssetsTotal, updatedVault.AssetsAvailable)
	}
	assetIssuance := clawbackTestIssuance(t, f.view, f.assetMPTID)
	if assetIssuance.OutstandingAmount != 60 {
		t.Fatalf("asset outstanding = %d, want 60", assetIssuance.OutstandingAmount)
	}
	assetToken := clawbackTestToken(t, f.view, f.assetMPTID, f.pseudoID)
	if assetToken.MPTAmount != 60 {
		t.Fatalf("vault asset balance = %d, want 60", assetToken.MPTAmount)
	}
	shareIssuance := clawbackTestIssuance(t, f.view, f.shareMPTID)
	if shareIssuance.OutstandingAmount != 60 {
		t.Fatalf("share outstanding = %d, want 60", shareIssuance.OutstandingAmount)
	}
	shareToken := clawbackTestToken(t, f.view, f.shareMPTID, f.holderID)
	if shareToken.MPTAmount != 60 {
		t.Fatalf("holder shares = %d, want 60", shareToken.MPTAmount)
	}
}

func TestVaultClawbackExplicitAmountDoesNotClampToHolderShares(t *testing.T) {
	f := newVaultClawbackFixture(t, 10)
	amount := state.NewMPTAmountWithIssuanceID(
		20,
		clawbackTestAddress(t, f.assetIssuerID),
		hex.EncodeToString(f.assetMPTID[:]),
	)
	f.txn.Amount = &amount
	if got := f.txn.Apply(f.ctx); got != ter.TecINSUFFICIENT_FUNDS {
		t.Fatalf("Apply() = %v, want tecINSUFFICIENT_FUNDS", got)
	}
}

func TestVaultClawbackExplicitAmountRoundsSharesToNearest(t *testing.T) {
	f := newVaultClawbackFixture(t, 2)
	vd, err := readVault(f.view, f.vaultKey)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	vd.AssetsTotal = "3"
	vd.AssetsAvailable = "3"
	issuance := clawbackTestIssuance(t, f.view, f.shareMPTID)
	issuance.OutstandingAmount = 2
	amount := state.NewMPTAmountWithIssuanceID(
		1,
		clawbackTestAddress(t, f.assetIssuerID),
		hex.EncodeToString(f.assetMPTID[:]),
	)
	f.txn.Amount = &amount

	_, _, recovered, shares, result := f.txn.clawbackAmounts(
		f.ctx,
		vd,
		issuance,
		f.assetIssuerID,
		f.holderID,
	)
	if result != ter.TesSUCCESS {
		t.Fatalf("clawbackAmounts() = %v, want tesSUCCESS", result)
	}
	if shares != 1 {
		t.Fatalf("shares destroyed = %d, want nearest-rounded 1", shares)
	}
	want := state.NewXRPLNumberScaled(2, 0, vaultNumberScale(f.ctx.Rules()), state.RoundToNearest)
	if recovered.Cmp(want) != 0 {
		t.Fatalf("assets recovered = %s, want asset-rounded 2", recovered.String())
	}
}

func TestVaultClawbackRejectsNegativeVaultAssetBalance(t *testing.T) {
	f := newVaultClawbackFixture(t, 100)
	issuerAddress := clawbackTestAddress(t, f.assetIssuerID)
	pseudoAddress := clawbackTestAddress(t, f.pseudoID)
	vaultBytes, err := serializeVaultForRules(&vaultData{
		Owner:            f.ownerID,
		Account:          f.pseudoID,
		Sequence:         1,
		ShareMPTID:       f.shareMPTID,
		Asset:            tx.Asset{Currency: "USD", Issuer: issuerAddress},
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
		AssetsTotal:      "100",
		AssetsAvailable:  "100",
	}, f.ctx.Rules())
	if err != nil {
		t.Fatalf("serialize IOU vault: %v", err)
	}
	f.view.data[f.vaultKey.Key] = vaultBytes
	line, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(-10, 0, "USD", issuerAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", issuerAddress),
		HighLimit: state.NewIssuedAmountFromValue(1_000, 0, "USD", pseudoAddress),
	})
	if err != nil {
		t.Fatalf("serialize vault trust line: %v", err)
	}
	f.view.data[keylet.Line(f.assetIssuerID, f.pseudoID, "USD").Key] = line
	deleteTestPutAccount(t, f.view, f.assetIssuerID, f.ctx.Account)
	deleteTestPutAccount(t, f.view, f.pseudoID, &state.AccountRoot{Account: pseudoAddress})

	amount := state.NewIssuedAmountFromValue(40, 0, "USD", issuerAddress)
	f.txn.Amount = &amount
	if got := f.txn.Apply(f.ctx); got != ter.TefINTERNAL {
		t.Fatalf("Apply() = %v, want tefINTERNAL", got)
	}
}

func clawbackTestID(b byte) [20]byte {
	var id [20]byte
	for i := range id {
		id[i] = b
	}
	return id
}

func clawbackTestAddress(t *testing.T, id [20]byte) string {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	return address
}

func clawbackTestIssuance(t *testing.T, view *mptArmsView, id [24]byte) *state.MPTokenIssuanceData {
	t.Helper()
	issuance, err := state.ParseMPTokenIssuance(view.data[keylet.MPTIssuance(id).Key])
	if err != nil {
		t.Fatalf("parse MPT issuance: %v", err)
	}
	return issuance
}

func clawbackTestToken(t *testing.T, view *mptArmsView, id [24]byte, holder [20]byte) *state.MPTokenData {
	t.Helper()
	token, err := state.ParseMPToken(view.data[keylet.MPTokenByID(id, holder).Key])
	if err != nil {
		t.Fatalf("parse MPToken: %v", err)
	}
	return token
}

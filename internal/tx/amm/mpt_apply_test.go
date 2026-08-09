package amm

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

type ammMPTView struct {
	data  map[[32]byte][]byte
	rules *amendment.Rules
}

func newAMMMPTView() *ammMPTView {
	rules := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureMPTokensV2).
		Build()
	return &ammMPTView{data: make(map[[32]byte][]byte), rules: rules}
}

func (v *ammMPTView) Read(k keylet.Keylet) ([]byte, error) { return v.data[k.Key], nil }
func (v *ammMPTView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}
func (v *ammMPTView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *ammMPTView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *ammMPTView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}
func (v *ammMPTView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (v *ammMPTView) ForEach(fn func([32]byte, []byte) bool) error {
	for k, data := range v.data {
		if !fn(k, data) {
			break
		}
	}
	return nil
}
func (v *ammMPTView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *ammMPTView) TxExists([32]byte) (bool, error)                           { return false, nil }
func (v *ammMPTView) Rules() *amendment.Rules                                   { return v.rules }
func (v *ammMPTView) LedgerSeq() uint32                                         { return 1 }
func (v *ammMPTView) AdjustOwnerCount([20]byte, tx.OwnerCounts, tx.OwnerCounts) {}

func ammMPTID(sequence uint32, issuer [20]byte) [24]byte {
	var id [24]byte
	binary.BigEndian.PutUint32(id[:4], sequence)
	copy(id[4:], issuer[:])
	return id
}

func insertAMMMPTAccount(t *testing.T, view *ammMPTView, id [20]byte, balance uint64, ownerCount uint32) *state.AccountRoot {
	t.Helper()
	account := &state.AccountRoot{
		Account:    state.EncodeAccountIDSafe(id),
		Balance:    balance,
		OwnerCount: ownerCount,
		Sequence:   1,
	}
	raw, err := state.SerializeAccountRoot(account)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(id), raw))
	return account
}

func insertAMMMPTIssuance(t *testing.T, view *ammMPTView, id [24]byte, flags uint32, transferFee uint16) {
	t.Helper()
	maximum := uint64(1_000_000)
	issuance := &state.MPTokenIssuanceData{
		Issuer:        mptutil.Issuer(id),
		Sequence:      binary.BigEndian.Uint32(id[:4]),
		MaximumAmount: &maximum,
		TransferFee:   transferFee,
		Flags:         flags,
	}
	raw, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), raw))
}

func readAMMMPTAccount(t *testing.T, view *ammMPTView, id [20]byte) *state.AccountRoot {
	t.Helper()
	raw, err := view.Read(keylet.Account(id))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(raw)
	require.NoError(t, err)
	return account
}

func ammMPTContext(view *ammMPTView, account *state.AccountRoot, accountID [20]byte) *tx.ApplyContext {
	return &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: accountID,
		Config: tx.EngineConfig{
			ReserveBase:      10_000_000,
			ReserveIncrement: 2_000_000,
			LedgerSequence:   1,
			Rules:            view.rules,
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}
}

func TestAMMCreateMPTApplyAndDelete(t *testing.T) {
	view := newAMMMPTView()
	var issuerID, creatorID [20]byte
	copy(issuerID[:], []byte("amm-mpt-issuer-00001"))
	copy(creatorID[:], []byte("amm-mpt-creator-0001"))
	issuerAddr := state.EncodeAccountIDSafe(issuerID)
	creatorAddr := state.EncodeAccountIDSafe(creatorID)
	insertAMMMPTAccount(t, view, issuerID, 100_000_000, 0)
	insertAMMMPTAccount(t, view, creatorID, 100_000_000, 0)

	id := ammMPTID(7, issuerID)
	idHex := mptutil.EncodeID(id)
	issuanceFlags := entry.LsfMPTRequireAuth | entry.LsfMPTCanTrade |
		entry.LsfMPTCanTransfer | entry.LsfMPTCanClawback
	insertAMMMPTIssuance(t, view, id, issuanceFlags, 5_000)
	require.Equal(t, ter.TesSUCCESS,
		mptutil.EnsureHolding(view, id, creatorID, entry.LsfMPTAuthorized, true))
	require.Equal(t, ter.TesSUCCESS, mptutil.Credit(view, id, issuerID, creatorID, 2_000, false))

	creator := readAMMMPTAccount(t, view, creatorID)
	mptAsset := tx.Asset{MPTIssuanceID: idHex}
	xrpAsset := tx.Asset{Currency: "XRP"}
	mptDeposit := state.NewMPTAmountWithIssuanceID(1_000, issuerAddr, idHex)
	xrpDeposit := state.NewXRPAmountFromInt(1_000_000)
	create := NewAMMCreate(creatorAddr, mptDeposit, xrpDeposit, 300)
	config := tx.EngineConfig{
		ReserveBase:      10_000_000,
		ReserveIncrement: 2_000_000,
		LedgerSequence:   1,
		Rules:            view.rules,
	}

	require.Equal(t, ter.TesSUCCESS, create.Preclaim(view, config))
	issuance, _, result := mptutil.ReadIssuance(view, id)
	require.Equal(t, ter.TesSUCCESS, result)
	issuance.Flags |= entry.LsfMPTLocked
	raw, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.MPTIssuance(id), raw))
	require.Equal(t, ter.TecLOCKED, create.Preclaim(view, config))
	issuance.Flags &^= entry.LsfMPTLocked
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.MPTIssuance(id), raw))

	ctx := ammMPTContext(view, creator, creatorID)
	require.Equal(t, ter.TesSUCCESS, create.Apply(ctx))

	ammKey := computeAMMKeylet(mptAsset, xrpAsset)
	ammRaw, err := view.Read(ammKey)
	require.NoError(t, err)
	amm, err := parseAMMData(ammRaw)
	require.NoError(t, err)
	holding, _, result := mptutil.ReadHolding(view, id, amm.Account)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, uint64(1_000), holding.MPTAmount)
	require.Equal(t, entry.LsfMPTAMM|entry.LsfMPTAuthorized, holding.Flags)

	creatorHolding, _, result := mptutil.ReadHolding(view, id, creatorID)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, uint64(1_000), creatorHolding.MPTAmount)
	ammAccount := readAMMMPTAccount(t, view, amm.Account)
	require.Zero(t, ammAccount.OwnerCount)
	require.Equal(t, uint64(1_000_000), ammAccount.Balance)

	claw := NewAMMClawback(issuerAddr, creatorAddr, mptAsset, xrpAsset)
	clawAmount := state.NewMPTAmountWithIssuanceID(100, issuerAddr, idHex)
	claw.Amount = &clawAmount
	require.NoError(t, claw.Validate())
	require.Equal(t, ter.TesSUCCESS, claw.Preclaim(view, config))
	issuance, _, result = mptutil.ReadIssuance(view, id)
	require.Equal(t, ter.TesSUCCESS, result)
	issuance.Flags &^= entry.LsfMPTCanClawback
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.MPTIssuance(id), raw))
	require.Equal(t, ter.TecNO_PERMISSION, claw.Preclaim(view, config))
	issuance.Flags |= entry.LsfMPTCanClawback
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.MPTIssuance(id), raw))
	issuer := readAMMMPTAccount(t, view, issuerID)
	require.Equal(t, ter.TesSUCCESS, claw.Apply(ammMPTContext(view, issuer, issuerID)))
	creatorHolding, _, result = mptutil.ReadHolding(view, id, creatorID)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, uint64(1_000), creatorHolding.MPTAmount)
	holding, _, result = mptutil.ReadHolding(view, id, amm.Account)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Less(t, holding.MPTAmount, uint64(1_000))
	require.NotZero(t, holding.MPTAmount)
	issuance, _, result = mptutil.ReadIssuance(view, id)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, creatorHolding.MPTAmount+holding.MPTAmount, issuance.OutstandingAmount)

	holding.MPTAmount = 0
	raw, err = state.SerializeMPToken(holding)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.MPTokenByID(id, amm.Account), raw))
	ammAccount.Balance = 0
	raw, err = state.SerializeAccountRoot(ammAccount)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.Account(amm.Account), raw))
	amm.LPTokenBalance = zeroAmount(tx.Asset{
		Currency: amm.LPTokenBalance.Currency,
		Issuer:   amm.LPTokenBalance.Issuer,
	})
	raw, err = serializeAMMData(amm)
	require.NoError(t, err)
	require.NoError(t, view.Update(ammKey, raw))

	lptKey := keylet.Line(creatorID, amm.Account, amm.LPTokenBalance.Currency)
	lptRaw, err := view.Read(lptKey)
	require.NoError(t, err)
	lptLine, err := state.ParseRippleState(lptRaw)
	require.NoError(t, err)
	lptLine.Balance, err = lptLine.Balance.Sub(lptLine.Balance)
	require.NoError(t, err)
	lptRaw, err = state.SerializeRippleState(lptLine)
	require.NoError(t, err)
	require.NoError(t, view.Update(lptKey, lptRaw))

	require.Equal(t, ter.TesSUCCESS, DeleteAMMAccount(view, mptAsset, xrpAsset))
	exists, err := view.Exists(keylet.MPTokenByID(id, amm.Account))
	require.NoError(t, err)
	require.False(t, exists)
	exists, err = view.Exists(ammKey)
	require.NoError(t, err)
	require.False(t, exists)
	exists, err = view.Exists(keylet.Account(amm.Account))
	require.NoError(t, err)
	require.False(t, exists)
}

func TestAMMWithdrawCreatesMPTWithWaivedTransferFee(t *testing.T) {
	view := newAMMMPTView()
	var issuerID, ammAccountID, destinationID [20]byte
	copy(issuerID[:], []byte("withdraw-mpt-issuer1"))
	copy(ammAccountID[:], []byte("withdraw-mpt-amm-001"))
	copy(destinationID[:], []byte("withdraw-mpt-dest-01"))
	insertAMMMPTAccount(t, view, issuerID, 100_000_000, 0)
	insertAMMMPTAccount(t, view, ammAccountID, 0, 0)
	destination := insertAMMMPTAccount(t, view, destinationID, 100_000_000, 2)

	id := ammMPTID(9, issuerID)
	insertAMMMPTIssuance(t, view, id, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, 5_000)
	require.Equal(t, ter.TesSUCCESS,
		mptutil.EnsureHolding(view, id, ammAccountID, entry.LsfMPTAMM|entry.LsfMPTAuthorized, false))
	require.Equal(t, ter.TesSUCCESS, mptutil.Credit(view, id, issuerID, ammAccountID, 100, false))

	ctx := ammMPTContext(view, destination, destinationID)
	asset := tx.Asset{MPTIssuanceID: mptutil.EncodeID(id)}
	amount := mptAmount(asset, 25)
	require.Equal(t, ter.TesSUCCESS,
		withdrawAssetToAccount(ctx, destinationID, ammAccountID, asset, amount, true))

	ammHolding, _, result := mptutil.ReadHolding(view, id, ammAccountID)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, uint64(75), ammHolding.MPTAmount)
	destinationHolding, _, result := mptutil.ReadHolding(view, id, destinationID)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, uint64(25), destinationHolding.MPTAmount)
	require.Zero(t, destinationHolding.Flags)
	require.Equal(t, uint32(3), ctx.Account.OwnerCount)
}

func TestAMMInsufficientBalancePropagatesCorruptMPTHolding(t *testing.T) {
	view := newAMMMPTView()
	var issuerID, holderID [20]byte
	copy(issuerID[:], []byte("amm-mpt-issuer-00001"))
	copy(holderID[:], []byte("amm-mpt-holder-00001"))
	insertAMMMPTAccount(t, view, issuerID, 100_000_000, 0)
	insertAMMMPTAccount(t, view, holderID, 100_000_000, 0)
	id := ammMPTID(7, issuerID)
	insertAMMMPTIssuance(t, view, id, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, 0)
	view.data[keylet.MPTokenByID(id, holderID).Key] = []byte{1}
	amount := state.NewMPTAmountWithIssuanceID(
		1,
		state.EncodeAccountIDSafe(issuerID),
		mptutil.EncodeID(id),
	)

	insufficient, result := insufficientBalance(view, holderID, amount, 0)
	require.False(t, insufficient)
	require.Equal(t, ter.TefINTERNAL, result)
}

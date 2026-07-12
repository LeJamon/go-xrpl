package check

import (
	"context"
	"encoding/hex"
	"math"
	"strings"
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
)

type checkMPTView struct {
	data map[[32]byte][]byte
}

func newCheckMPTView() *checkMPTView { return &checkMPTView{data: make(map[[32]byte][]byte)} }

func (v *checkMPTView) Read(k keylet.Keylet) ([]byte, error)      { return v.data[k.Key], nil }
func (v *checkMPTView) Exists(k keylet.Keylet) (bool, error)      { _, ok := v.data[k.Key]; return ok, nil }
func (v *checkMPTView) Insert(k keylet.Keylet, data []byte) error { v.data[k.Key] = data; return nil }
func (v *checkMPTView) Update(k keylet.Keylet, data []byte) error { v.data[k.Key] = data; return nil }
func (v *checkMPTView) Erase(k keylet.Keylet) error               { delete(v.data, k.Key); return nil }
func (v *checkMPTView) AdjustDropsDestroyed(drops.XRPAmount)      {}
func (v *checkMPTView) ForEach(fn func([32]byte, []byte) bool) error {
	for k, data := range v.data {
		if !fn(k, data) {
			break
		}
	}
	return nil
}
func (v *checkMPTView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *checkMPTView) TxExists([32]byte) bool  { return false }
func (v *checkMPTView) Rules() *amendment.Rules { return amendment.AllSupportedRules() }
func (v *checkMPTView) LedgerSeq() uint32       { return 1 }

func checkMPTAccountID(seed byte) [20]byte {
	var id [20]byte
	for i := range id {
		id[i] = seed
	}
	return id
}

func putCheckMPTAccount(t *testing.T, view *checkMPTView, id [20]byte, ownerCount uint32) *state.AccountRoot {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatal(err)
	}
	account := &state.AccountRoot{Account: address, Balance: 1_000_000_000, Sequence: 1, OwnerCount: ownerCount}
	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(keylet.Account(id), data); err != nil {
		t.Fatal(err)
	}
	return account
}

func putCheckMPTIssuance(t *testing.T, view *checkMPTView, id [24]byte, issuer [20]byte, flags uint32, transferFee uint16, outstanding uint64) {
	t.Helper()
	maximum := max(uint64(10_000), outstanding)
	data, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          1,
		Flags:             flags,
		TransferFee:       transferFee,
		OutstandingAmount: outstanding,
		MaximumAmount:     &maximum,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(keylet.MPTIssuance(id), data); err != nil {
		t.Fatal(err)
	}
}

func putCheckMPTHolding(t *testing.T, view *checkMPTView, id [24]byte, holder [20]byte, amount uint64, flags uint32) {
	t.Helper()
	data, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		MPTAmount:         amount,
		Flags:             flags,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(keylet.MPTokenByID(id, holder), data); err != nil {
		t.Fatal(err)
	}
}

func checkMPTContext(view *checkMPTView, account *state.AccountRoot, accountID [20]byte) *tx.ApplyContext {
	return &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: accountID,
		Config: tx.EngineConfig{
			ReserveBase:      10_000_000,
			ReserveIncrement: 2_000_000,
			Rules:            amendment.AllSupportedRules(),
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}
}

func TestCheckCreateStoresMPTSendMaxAndChecksLock(t *testing.T) {
	issuerID := checkMPTAccountID(0x11)
	srcID := checkMPTAccountID(0x22)
	dstID := checkMPTAccountID(0x33)
	mptID := keylet.MakeMPTID(1, issuerID)
	mptHex := hex.EncodeToString(mptID[:])

	build := func(flags uint32) (*checkMPTView, *tx.ApplyContext, *CheckCreate) {
		view := newCheckMPTView()
		src := putCheckMPTAccount(t, view, srcID, 0)
		putCheckMPTAccount(t, view, dstID, 0)
		putCheckMPTIssuance(t, view, mptID, issuerID, flags, 0, 0)
		srcAddress, _ := state.EncodeAccountID(srcID)
		dstAddress, _ := state.EncodeAccountID(dstID)
		amount := state.NewMPTAmountWithIssuanceID(100, "", mptHex)
		create := NewCheckCreate(srcAddress, dstAddress, amount)
		if err := create.Validate(); err != nil {
			t.Fatalf("validate MPT check: %v", err)
		}
		sequence := uint32(7)
		create.Sequence = &sequence
		return view, checkMPTContext(view, src, srcID), create
	}

	view, ctx, create := build(entry.LsfMPTCanTransfer)
	if result := create.Apply(ctx); result != ter.TesSUCCESS {
		t.Fatalf("create MPT check: got %v", result)
	}
	checkData, err := view.Read(keylet.Check(srcID, 7))
	if err != nil || checkData == nil {
		t.Fatal("MPT Check SLE was not stored")
	}
	stored, err := state.ParseCheck(checkData)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SendMaxAmount.IsMPT() || stored.SendMaxAmount.MPTIssuanceID() != strings.ToUpper(mptHex) {
		t.Fatalf("stored SendMax lost MPT identity: %#v", stored.SendMaxAmount)
	}

	_, lockedCtx, lockedCreate := build(entry.LsfMPTCanTransfer | entry.LsfMPTLocked)
	if result := lockedCreate.Apply(lockedCtx); result != ter.TecLOCKED {
		t.Fatalf("locked MPT: got %v, want tecLOCKED", result)
	}
}

func TestCheckCreateAllowsMissingIOUIssuer(t *testing.T) {
	srcID := checkMPTAccountID(0x34)
	dstID := checkMPTAccountID(0x35)
	issuerID := checkMPTAccountID(0x36)
	view := newCheckMPTView()
	src := putCheckMPTAccount(t, view, srcID, 0)
	putCheckMPTAccount(t, view, dstID, 0)

	srcAddress := state.EncodeAccountIDSafe(srcID)
	dstAddress := state.EncodeAccountIDSafe(dstID)
	issuerAddress := state.EncodeAccountIDSafe(issuerID)
	create := NewCheckCreate(
		srcAddress,
		dstAddress,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddress),
	)
	sequence := uint32(8)
	create.Sequence = &sequence
	if err := create.Validate(); err != nil {
		t.Fatalf("validate check with missing IOU issuer: %v", err)
	}

	if result := create.Apply(checkMPTContext(view, src, srcID)); result != ter.TesSUCCESS {
		t.Fatalf("create check with missing IOU issuer = %v, want tesSUCCESS", result)
	}
	if exists, err := view.Exists(keylet.Check(srcID, sequence)); err != nil || !exists {
		t.Fatalf("created check missing: exists=%v err=%v", exists, err)
	}
}

func TestCheckCreateRejectsGloballyFrozenIOUIssuer(t *testing.T) {
	srcID := checkMPTAccountID(0x37)
	dstID := checkMPTAccountID(0x38)
	issuerID := checkMPTAccountID(0x39)
	view := newCheckMPTView()
	src := putCheckMPTAccount(t, view, srcID, 0)
	putCheckMPTAccount(t, view, dstID, 0)
	issuer := putCheckMPTAccount(t, view, issuerID, 0)
	issuer.Flags = state.LsfGlobalFreeze
	issuerData, err := state.SerializeAccountRoot(issuer)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Update(keylet.Account(issuerID), issuerData); err != nil {
		t.Fatal(err)
	}

	create := NewCheckCreate(
		state.EncodeAccountIDSafe(srcID),
		state.EncodeAccountIDSafe(dstID),
		state.NewIssuedAmountFromFloat64(100, "USD", state.EncodeAccountIDSafe(issuerID)),
	)
	if result := create.Apply(checkMPTContext(view, src, srcID)); result != ter.TecFROZEN {
		t.Fatalf("create check with globally frozen IOU issuer = %v, want tecFROZEN", result)
	}
}

func TestCheckCashMPTCreatesHoldingAndAppliesTransferFee(t *testing.T) {
	issuerID := checkMPTAccountID(0x41)
	srcID := checkMPTAccountID(0x42)
	dstID := checkMPTAccountID(0x43)
	mptID := keylet.MakeMPTID(1, issuerID)
	mptHex := strings.ToUpper(hex.EncodeToString(mptID[:]))
	view := newCheckMPTView()
	putCheckMPTAccount(t, view, issuerID, 1)
	putCheckMPTAccount(t, view, srcID, 2)
	dst := putCheckMPTAccount(t, view, dstID, 0)
	putCheckMPTIssuance(t, view, mptID, issuerID, entry.LsfMPTCanTransfer, 25_000, 1_000)
	putCheckMPTHolding(t, view, mptID, srcID, 1_000, 0)

	checkKey := keylet.Check(srcID, 9)
	srcDir, err := state.DirInsert(view, keylet.OwnerDir(srcID), checkKey.Key, false, func(dir *state.DirectoryNode) { dir.Owner = srcID })
	if err != nil {
		t.Fatal(err)
	}
	dstDir, err := state.DirInsert(view, keylet.OwnerDir(dstID), checkKey.Key, false, func(dir *state.DirectoryNode) { dir.Owner = dstID })
	if err != nil {
		t.Fatal(err)
	}
	check := &state.CheckData{
		Account:         srcID,
		DestinationID:   dstID,
		Sequence:        9,
		OwnerNode:       srcDir.Page,
		DestinationNode: dstDir.Page,
		HasDestNode:     true,
		SendMaxAmount:   state.NewMPTAmountWithIssuanceID(125, "", mptHex),
	}
	checkData, err := state.SerializeCheckFromData(check)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(checkKey, checkData); err != nil {
		t.Fatal(err)
	}

	deliverMin := state.NewMPTAmountWithIssuanceID(75, "", mptHex)
	cash := &CheckCash{DeliverMin: &deliverMin}
	ctx := checkMPTContext(view, dst, dstID)
	if result := cash.applyCashMPTAmount(ctx, check, checkKey, deliverMin, true); result != ter.TesSUCCESS {
		t.Fatalf("cash MPT check: got %v", result)
	}

	sourceToken, _, result := mptutil.ReadHolding(view, mptID, srcID)
	if result != ter.TesSUCCESS || sourceToken.MPTAmount != 875 {
		t.Fatalf("source holding = %v/%v, want 875", sourceToken, result)
	}
	destToken, _, result := mptutil.ReadHolding(view, mptID, dstID)
	if result != ter.TesSUCCESS || destToken.MPTAmount != 100 {
		t.Fatalf("destination holding = %v/%v, want 100", destToken, result)
	}
	if ctx.Account.OwnerCount != 1 {
		t.Fatalf("destination owner count = %d, want 1", ctx.Account.OwnerCount)
	}
	if ctx.Metadata.DeliveredAmount == nil || ctx.Metadata.DeliveredAmount.Value() != "100" {
		t.Fatalf("delivered amount = %v, want 100", ctx.Metadata.DeliveredAmount)
	}
	if exists, _ := view.Exists(checkKey); exists {
		t.Fatal("cashed check was not erased")
	}
}

func TestCheckCashMPTTransferFeeRoundsToNearest(t *testing.T) {
	issuerID := checkMPTAccountID(0x61)
	srcID := checkMPTAccountID(0x62)
	dstID := checkMPTAccountID(0x63)
	mptID := keylet.MakeMPTID(1, issuerID)
	mptHex := mptutil.EncodeID(mptID)
	view := newCheckMPTView()
	putCheckMPTAccount(t, view, issuerID, 1)
	putCheckMPTAccount(t, view, srcID, 1)
	dst := putCheckMPTAccount(t, view, dstID, 1)
	putCheckMPTIssuance(t, view, mptID, issuerID, entry.LsfMPTCanTransfer, 25_000, 1)
	putCheckMPTHolding(t, view, mptID, srcID, 1, 0)
	putCheckMPTHolding(t, view, mptID, dstID, 0, 0)

	checkKey := keylet.Check(srcID, 1)
	srcDir, err := state.DirInsert(view, keylet.OwnerDir(srcID), checkKey.Key, false, func(dir *state.DirectoryNode) { dir.Owner = srcID })
	if err != nil {
		t.Fatal(err)
	}
	dstDir, err := state.DirInsert(view, keylet.OwnerDir(dstID), checkKey.Key, false, func(dir *state.DirectoryNode) { dir.Owner = dstID })
	if err != nil {
		t.Fatal(err)
	}
	amount := state.NewMPTAmountWithIssuanceID(1, "", mptHex)
	check := &state.CheckData{
		Account:         srcID,
		DestinationID:   dstID,
		Sequence:        1,
		OwnerNode:       srcDir.Page,
		DestinationNode: dstDir.Page,
		HasDestNode:     true,
		SendMaxAmount:   amount,
	}
	checkData, err := state.SerializeCheckFromData(check)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(checkKey, checkData); err != nil {
		t.Fatal(err)
	}
	cash := &CheckCash{Amount: &amount}
	if result := cash.applyCashMPTAmount(checkMPTContext(view, dst, dstID), check, checkKey, amount, false); result != ter.TesSUCCESS {
		t.Fatalf("cash MPT check = %v, want tesSUCCESS", result)
	}
	source, _, result := mptutil.ReadHolding(view, mptID, srcID)
	if result != ter.TesSUCCESS || source.MPTAmount != 0 {
		t.Fatalf("source holding = %v/%v, want 0", source, result)
	}
	destination, _, result := mptutil.ReadHolding(view, mptID, dstID)
	if result != ter.TesSUCCESS || destination.MPTAmount != 1 {
		t.Fatalf("destination holding = %v/%v, want 1", destination, result)
	}
}

func TestCheckCashMPTTransferFeeOverflowReturnsTefException(t *testing.T) {
	issuerID := checkMPTAccountID(0x64)
	srcID := checkMPTAccountID(0x65)
	dstID := checkMPTAccountID(0x66)
	mptID := keylet.MakeMPTID(1, issuerID)
	mptHex := mptutil.EncodeID(mptID)
	view := newCheckMPTView()
	putCheckMPTAccount(t, view, issuerID, 1)
	putCheckMPTAccount(t, view, srcID, 1)
	dst := putCheckMPTAccount(t, view, dstID, 1)
	putCheckMPTIssuance(t, view, mptID, issuerID, entry.LsfMPTCanTransfer, 25_000, math.MaxInt64)
	putCheckMPTHolding(t, view, mptID, srcID, math.MaxInt64, 0)
	putCheckMPTHolding(t, view, mptID, dstID, 0, 0)

	amount := state.NewMPTAmountWithIssuanceID(math.MaxInt64, "", mptHex)
	check := &state.CheckData{Account: srcID, DestinationID: dstID, SendMaxAmount: amount}
	cash := &CheckCash{Amount: &amount}
	result := cash.applyCashMPTAmount(
		checkMPTContext(view, dst, dstID),
		check,
		keylet.Check(srcID, 1),
		amount,
		false,
	)
	if result != ter.TefEXCEPTION {
		t.Fatalf("cash MPT check = %v, want tefEXCEPTION", result)
	}

	source, _, result := mptutil.ReadHolding(view, mptID, srcID)
	if result != ter.TesSUCCESS || source.MPTAmount != math.MaxInt64 {
		t.Fatalf("source holding changed: %v/%v", source, result)
	}
	destination, _, result := mptutil.ReadHolding(view, mptID, dstID)
	if result != ter.TesSUCCESS || destination.MPTAmount != 0 {
		t.Fatalf("destination holding changed: %v/%v", destination, result)
	}
}

func TestCheckCashMissingMPTIssuanceIsPathPartial(t *testing.T) {
	issuerID := checkMPTAccountID(0x71)
	srcID := checkMPTAccountID(0x72)
	dstID := checkMPTAccountID(0x73)
	mptID := keylet.MakeMPTID(1, issuerID)
	amount := state.NewMPTAmountWithIssuanceID(1, "", mptutil.EncodeID(mptID))
	check := &state.CheckData{Account: srcID, DestinationID: dstID, SendMaxAmount: amount}

	result := (&CheckCash{Amount: &amount}).applyCashMPTAmount(
		checkMPTContext(newCheckMPTView(), &state.AccountRoot{}, dstID),
		check,
		keylet.Check(srcID, 1),
		amount,
		false,
	)
	if result != ter.TecPATH_PARTIAL {
		t.Fatalf("cash missing MPT issuance = %v, want tecPATH_PARTIAL", result)
	}
}

func TestCheckCashRejectsIllegalStoredMPT(t *testing.T) {
	srcID := checkMPTAccountID(0x81)
	dstID := checkMPTAccountID(0x82)
	issuerID := checkMPTAccountID(0x83)
	mptID := keylet.MakeMPTID(1, issuerID)
	mptHex := mptutil.EncodeID(mptID)
	view := newCheckMPTView()
	putCheckMPTAccount(t, view, srcID, 1)
	dst := putCheckMPTAccount(t, view, dstID, 0)
	checkKey := keylet.Check(srcID, 1)
	check := &state.CheckData{
		Account:       srcID,
		DestinationID: dstID,
		SendMaxAmount: state.NewMPTAmountWithIssuanceID(-1, "", mptHex),
	}
	checkData, err := state.SerializeCheckFromData(check)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(checkKey, checkData); err != nil {
		t.Fatal(err)
	}

	dstAddress, _ := state.EncodeAccountID(dstID)
	amount := state.NewMPTAmountWithIssuanceID(1, "", mptHex)
	cash := NewCheckCash(dstAddress, hex.EncodeToString(checkKey.Key[:]))
	cash.Amount = &amount
	if result := cash.Apply(checkMPTContext(view, dst, dstID)); result != ter.TefBAD_LEDGER {
		t.Fatalf("cash illegal stored MPT = %v, want tefBAD_LEDGER", result)
	}
}

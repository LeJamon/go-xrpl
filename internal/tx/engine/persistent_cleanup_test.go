package engine

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
)

func ledgerTypeBytes(t ledgerentry.Type) []byte {
	data := []byte{0x11, 0, 0}
	binary.BigEndian.PutUint16(data[1:], uint16(t))
	return data
}

func persistentTestOffer(t *testing.T, pays int64) []byte {
	t.Helper()
	data, err := state.SerializeLedgerOffer(&state.LedgerOffer{
		Account:   recoveryTestAccount,
		Sequence:  1,
		TakerPays: state.NewXRPAmountFromInt(pays),
		TakerGets: state.NewXRPAmountFromInt(100),
	})
	if err != nil {
		t.Fatalf("SerializeLedgerOffer: %v", err)
	}
	return data
}

func TestCollectPersistentDeletionsResultMatrix(t *testing.T) {
	table := applystate.NewApplyStateTable(newRecordingBaseView(), [32]byte{}, 1, nil)
	keys := struct {
		unfundedOffer [32]byte
		fundedOffer   [32]byte
		trustLine     [32]byte
		nftOffer      [32]byte
		credential    [32]byte
		mpt           [32]byte
	}{
		unfundedOffer: [32]byte{1},
		fundedOffer:   [32]byte{2},
		trustLine:     [32]byte{3},
		nftOffer:      [32]byte{4},
		credential:    [32]byte{5},
		mpt:           [32]byte{6},
	}
	items := table.GetItems()
	unchangedOffer := persistentTestOffer(t, 100)
	items[keys.unfundedOffer] = &applystate.TrackedEntry{
		Action: applystate.ActionErase, Original: unchangedOffer, Current: unchangedOffer,
	}
	items[keys.fundedOffer] = &applystate.TrackedEntry{
		Action:   applystate.ActionErase,
		Original: persistentTestOffer(t, 100),
		Current:  persistentTestOffer(t, 50),
	}
	items[keys.trustLine] = erasedType(ledgerentry.TypeRippleState)
	items[keys.nftOffer] = erasedType(ledgerentry.TypeNFTokenOffer)
	items[keys.credential] = erasedType(ledgerentry.TypeCredential)
	items[keys.mpt] = erasedType(ledgerentry.TypeMPToken)

	tests := []struct {
		name        string
		result      ter.Result
		offers      [][32]byte
		trustLines  [][32]byte
		nftOffers   [][32]byte
		credentials [][32]byte
	}{
		{name: "oversize", result: ter.TecOVERSIZE, offers: [][32]byte{keys.unfundedOffer}},
		{name: "killed", result: ter.TecKILLED, offers: [][32]byte{keys.unfundedOffer}},
		{name: "incomplete", result: ter.TecINCOMPLETE, trustLines: [][32]byte{keys.trustLine}},
		{name: "expired", result: ter.TecEXPIRED, nftOffers: [][32]byte{keys.nftOffer}, credentials: [][32]byte{keys.credential}},
		{name: "generic tec", result: ter.TecPATH_DRY},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collectPersistentDeletions(table, tc.result)
			assertKeysEqual(t, "offers", got.offers, tc.offers)
			assertKeysEqual(t, "trust lines", got.trustLines, tc.trustLines)
			assertKeysEqual(t, "NFT offers", got.nftOffers, tc.nftOffers)
			assertKeysEqual(t, "credentials", got.credentials, tc.credentials)
		})
	}
}

func TestIsUnfundedOfferDeletionIgnoresIOUIssuer(t *testing.T) {
	issuerA := recoveryTestAccount
	issuerB := state.EncodeAccountIDSafe([20]byte{1})
	serialize := func(issuer string) []byte {
		data, err := state.SerializeLedgerOffer(&state.LedgerOffer{
			Account:   recoveryTestAccount,
			Sequence:  1,
			TakerPays: state.NewIssuedAmountFromValue(100, 0, "USD", issuer),
			TakerGets: state.NewXRPAmountFromInt(100),
		})
		if err != nil {
			t.Fatalf("SerializeLedgerOffer: %v", err)
		}
		return data
	}

	tracked := &applystate.TrackedEntry{
		Action:   applystate.ActionErase,
		Original: serialize(issuerA),
		Current:  serialize(issuerB),
	}
	if !isUnfundedOfferDeletion(tracked) {
		t.Fatal("equal IOU TakerPays values with different issuers must compare unchanged")
	}
}

func TestCollectPersistentDeletionsSortsAllReplayCandidates(t *testing.T) {
	tests := []struct {
		name      string
		result    ter.Result
		entryType ledgerentry.Type
		count     int
		selectKey func(persistentDeletions) [][32]byte
	}{
		{
			name: "offers over replay limit", result: ter.TecKILLED,
			entryType: ledgerentry.TypeOffer, count: unfundedOfferRemoveLimit + 1,
			selectKey: func(deleted persistentDeletions) [][32]byte { return deleted.offers },
		},
		{
			name: "NFT offers over replay limit", result: ter.TecEXPIRED,
			entryType: ledgerentry.TypeNFTokenOffer, count: expiredNFTokenOfferRemoveLimit + 1,
			selectKey: func(deleted persistentDeletions) [][32]byte { return deleted.nftOffers },
		},
		{
			name: "trust lines over replay limit", result: ter.TecINCOMPLETE,
			entryType: ledgerentry.TypeRippleState, count: maxDeletableAMMTrustLines + 1,
			selectKey: func(deleted persistentDeletions) [][32]byte { return deleted.trustLines },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			table := applystate.NewApplyStateTable(newRecordingBaseView(), [32]byte{}, 1, nil)
			for i := tc.count; i > 0; i-- {
				var key [32]byte
				binary.BigEndian.PutUint32(key[28:], uint32(i))
				if tc.entryType == ledgerentry.TypeOffer {
					offer := persistentTestOffer(t, 100)
					table.GetItems()[key] = &applystate.TrackedEntry{
						Action: applystate.ActionErase, Original: offer, Current: offer,
					}
				} else {
					table.GetItems()[key] = erasedType(tc.entryType)
				}
			}

			got := tc.selectKey(collectPersistentDeletions(table, tc.result))
			if len(got) != tc.count {
				t.Fatalf("candidate count = %d, want %d", len(got), tc.count)
			}
			for i := range got {
				want := uint32(i + 1)
				if binary.BigEndian.Uint32(got[i][28:]) != want {
					t.Fatalf("candidate[%d] = %d, want %d", i, binary.BigEndian.Uint32(got[i][28:]), want)
				}
			}
		})
	}
}

func TestReplayExistingWithLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{name: "offers", limit: unfundedOfferRemoveLimit},
		{name: "NFT offers", limit: expiredNFTokenOfferRemoveLimit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keys := make([][32]byte, tc.limit+2)
			var replayed [][32]byte
			replayExistingWithLimit(keys, tc.limit, func(key [32]byte) bool {
				if len(replayed) == 0 {
					replayed = append(replayed, key)
					return false
				}
				replayed = append(replayed, key)
				return true
			})
			if len(replayed) != tc.limit+1 {
				t.Fatalf("replay attempts = %d, want %d", len(replayed), tc.limit+1)
			}
		})
	}
}

func TestRemoveDeletedTrustLinesPreservesLineWhenAccountMissing(t *testing.T) {
	view := newRecordingBaseView()
	lowID := [20]byte{1}
	highID := [20]byte{2}
	lineKey := keylet.Keylet{Key: [32]byte{9}}
	lineData, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, 0, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, 0, "USD", state.EncodeAccountIDSafe(lowID)),
		HighLimit: state.NewIssuedAmountFromValue(0, 0, "USD", state.EncodeAccountIDSafe(highID)),
		LowNode:   0,
		HighNode:  0,
	})
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}
	if err := view.Insert(lineKey, lineData); err != nil {
		t.Fatalf("Insert RippleState: %v", err)
	}

	table := applystate.NewApplyStateTable(view, [32]byte{}, 1, amendment.AllSupportedRules())
	recoveryEngine(view, txcore.TapNONE).removeDeletedTrustLines(table, [][32]byte{lineKey.Key})
	got, err := table.Read(lineKey)
	if err != nil {
		t.Fatalf("Read RippleState: %v", err)
	}
	if !bytes.Equal(got, lineData) {
		t.Fatalf("RippleState changed when an endpoint account was missing: got %x, want %x", got, lineData)
	}
}

type eraseOfferTecTx struct {
	*txcore.BaseTx
	key  keylet.Keylet
	code ter.Result
}

func (tx eraseOfferTecTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	if err := ctx.View.Erase(tx.key); err != nil {
		return ter.TefINTERNAL
	}
	return tx.code
}

func putRecoverySponsoredOffer(
	t *testing.T,
	view *recordingBaseView,
	withDirectories bool,
) ([20]byte, keylet.Keylet) {
	t.Helper()
	sponsorID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID sponsor: %v", err)
	}
	sponsorKey := keylet.Account(sponsorID)
	sponsor := readRecoveryAccount(t, view, sponsorKey)
	sponsor.SponsoringOwnerCount = 1
	sponsorData, err := state.SerializeAccountRoot(sponsor)
	if err != nil {
		t.Fatalf("SerializeAccountRoot sponsor: %v", err)
	}
	if err := view.Update(sponsorKey, sponsorData); err != nil {
		t.Fatalf("Update sponsor: %v", err)
	}

	ownerID := [20]byte{7}
	ownerAddress := state.EncodeAccountIDSafe(ownerID)
	ownerData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:             ownerAddress,
		Balance:             1_000_000_000,
		Sequence:            2,
		OwnerCount:          1,
		SponsoredOwnerCount: 1,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot owner: %v", err)
	}
	if err := view.Insert(keylet.Account(ownerID), ownerData); err != nil {
		t.Fatalf("Insert owner: %v", err)
	}

	offerKey := keylet.Offer(ownerID, 1)
	bookDir := keylet.Keylet{Key: [32]byte{0x55, 2}}
	var ownerNode, bookNode uint64
	if withDirectories {
		ownerResult, dirErr := state.DirInsert(view, keylet.OwnerDir(ownerID), offerKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = ownerID
		})
		if dirErr != nil {
			t.Fatalf("DirInsert owner: %v", dirErr)
		}
		ownerNode = ownerResult.Page
		bookResult, dirErr := state.DirInsert(view, bookDir, offerKey.Key, true, func(dir *state.DirectoryNode) {
			dir.TakerPaysCurrency = keylet.CurrencyBytes("USD")
			dir.TakerPaysIssuer = sponsorID
		})
		if dirErr != nil {
			t.Fatalf("DirInsert book: %v", dirErr)
		}
		bookNode = bookResult.Page
	}
	offerData, err := state.SerializeLedgerOffer(&state.LedgerOffer{
		Account:       ownerAddress,
		Sequence:      1,
		TakerPays:     state.NewIssuedAmountFromValue(100, 0, "USD", recoveryTestAccount),
		TakerGets:     state.NewXRPAmountFromInt(100),
		BookDirectory: bookDir.Key,
		BookNode:      bookNode,
		OwnerNode:     ownerNode,
		Sponsor:       recoveryTestAccount,
	})
	if err != nil {
		t.Fatalf("SerializeLedgerOffer: %v", err)
	}
	if err := view.Insert(offerKey, offerData); err != nil {
		t.Fatalf("Insert offer: %v", err)
	}
	return ownerID, offerKey
}

func TestWorkOnTecOfferReplayAdjustsSponsoredCounters(t *testing.T) {
	for _, code := range []ter.Result{ter.TecKILLED, ter.TecOVERSIZE} {
		t.Run(code.String(), func(t *testing.T) {
			view := newRecordingBaseView()
			sponsorKey := fundRecoveryAccount(t, view, 1_000_000, 1)
			ownerID, offerKey := putRecoverySponsoredOffer(t, view, true)

			result := recoveryEngine(view, txcore.TapNONE).Apply(eraseOfferTecTx{
				BaseTx: recoveryTx(10, 1),
				key:    offerKey,
				code:   code,
			})

			if result.Result != code || !result.Applied {
				t.Fatalf("result/applied = %s/%v, want %s/true", result.Result, result.Applied, code)
			}
			offerData, err := view.Read(offerKey)
			if err != nil || offerData != nil {
				t.Fatalf("replayed offer = %x, err=%v, want deleted", offerData, err)
			}
			owner := readRecoveryAccount(t, view, keylet.Account(ownerID))
			if owner.OwnerCount != 0 || owner.SponsoredOwnerCount != 0 {
				t.Fatalf("owner counts = %d/%d, want 0/0", owner.OwnerCount, owner.SponsoredOwnerCount)
			}
			sponsor := readRecoveryAccount(t, view, sponsorKey)
			if sponsor.SponsoringOwnerCount != 0 {
				t.Fatalf("sponsor count = %d, want 0", sponsor.SponsoringOwnerCount)
			}
		})
	}
}

func TestWorkOnTecOfferReplayPropagatesCleanupFailure(t *testing.T) {
	view := newRecordingBaseView()
	sponsorKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	ownerID, offerKey := putRecoverySponsoredOffer(t, view, false)

	result := recoveryEngine(view, txcore.TapNONE).Apply(eraseOfferTecTx{
		BaseTx: recoveryTx(10, 1),
		key:    offerKey,
		code:   ter.TecKILLED,
	})

	if result.Result != ter.TefBAD_LEDGER || result.Applied {
		t.Fatalf("result/applied = %s/%v, want tefBAD_LEDGER/false", result.Result, result.Applied)
	}
	offerData, err := view.Read(offerKey)
	if err != nil || offerData == nil {
		t.Fatalf("offer after failed replay = %x, err=%v, want preserved", offerData, err)
	}
	owner := readRecoveryAccount(t, view, keylet.Account(ownerID))
	if owner.OwnerCount != 1 || owner.SponsoredOwnerCount != 1 {
		t.Fatalf("owner counts = %d/%d, want 1/1", owner.OwnerCount, owner.SponsoredOwnerCount)
	}
	sponsor := readRecoveryAccount(t, view, sponsorKey)
	if sponsor.SponsoringOwnerCount != 1 || sponsor.Balance != 1_000_000 || sponsor.Sequence != 1 {
		t.Fatalf("sponsor state = count %d balance %d sequence %d, want 1/1000000/1",
			sponsor.SponsoringOwnerCount, sponsor.Balance, sponsor.Sequence)
	}
}

func erasedType(t ledgerentry.Type) *applystate.TrackedEntry {
	data := ledgerTypeBytes(t)
	return &applystate.TrackedEntry{Action: applystate.ActionErase, Original: data, Current: data}
}

func assertKeysEqual(t *testing.T, name string, got, want [][32]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %x, want %x", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %x, want %x", name, i, got[i], want[i])
		}
	}
}

type eraseMPTokenTecTx struct {
	*txcore.BaseTx
	key keylet.Keylet
}

func (tx eraseMPTokenTecTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	if err := ctx.View.Erase(tx.key); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TecINCOMPLETE
}

func TestApplyTecIncompleteRollsBackDeletedMPToken(t *testing.T) {
	view := newRecordingBaseView()
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	holder, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	var issuanceID [24]byte
	issuanceID[0] = 1
	tokenKey := keylet.MPTokenByID(issuanceID, holder)
	tokenData, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: issuanceID,
		MPTAmount:         100,
	})
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	if err := view.Insert(tokenKey, tokenData); err != nil {
		t.Fatalf("Insert MPToken: %v", err)
	}

	result := recoveryEngine(view, txcore.TapNONE).Apply(eraseMPTokenTecTx{
		BaseTx: recoveryTx(10, 1),
		key:    tokenKey,
	})
	if result.Result != ter.TecINCOMPLETE || !result.Applied || result.Fee != 10 {
		t.Fatalf("result/applied/fee = %s/%v/%d, want tecINCOMPLETE/true/10",
			result.Result, result.Applied, result.Fee)
	}
	if result.Metadata == nil || result.Metadata.TransactionResult != ter.TecINCOMPLETE {
		t.Fatalf("metadata result = %v, want tecINCOMPLETE", result.Metadata)
	}
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType == ledgerentry.TypeMPToken.String() {
			t.Fatal("rolled-back MPToken deletion appeared in tecINCOMPLETE metadata")
		}
	}
	got, err := view.Read(tokenKey)
	if err != nil {
		t.Fatalf("Read MPToken: %v", err)
	}
	if !bytes.Equal(got, tokenData) {
		t.Fatalf("MPToken changed after tecINCOMPLETE: got %x, want %x", got, tokenData)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 999_990 || account.Sequence != 2 {
		t.Fatalf("payer balance/sequence = %d/%d, want 999990/2", account.Balance, account.Sequence)
	}
}

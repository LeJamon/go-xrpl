package account

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/oracle"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type AccountDelete struct {
	tx.BaseTx
	Destination    string   `json:"Destination" xrpl:"Destination"`
	DestinationTag *uint32  `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
	CredentialIDs  []string `json:"CredentialIDs,omitempty" xrpl:"CredentialIDs,omitempty"`
}

func NewAccountDelete(account, destination string) *AccountDelete {
	return &AccountDelete{BaseTx: *tx.NewBaseTx(tx.TypeAccountDelete, account), Destination: destination}
}

func (a *AccountDelete) TxType() tx.Type { return tx.TypeAccountDelete }

func (a *AccountDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureDeletableAccounts}
}

func (a *AccountDelete) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}
	if err := tx.CheckFlags(a.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}
	if a.Destination == "" {
		return tx.Errorf(tx.TemDST_NEEDED, "Destination is required")
	}
	if a.Account == a.Destination {
		return tx.Errorf(tx.TemDST_IS_SRC, "cannot delete account to self")
	}
	present := a.CredentialIDs != nil || a.GetCommon().HasField("CredentialIDs")
	if err := credential.CheckFields(a.CredentialIDs, present, "Duplicate credential ID"); err != nil {
		return err
	}
	return nil
}

func (a *AccountDelete) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	if view != nil {
		data, err := view.Read(keylet.Fees())
		if err == nil && data != nil {
			if fs, err := state.ParseFeeSettings(data); err == nil {
				return fs.GetReserveIncrement()
			}
		}
	}
	return config.ReserveIncrement
}

func (a *AccountDelete) Flatten() (map[string]any, error) { return tx.ReflectFlatten(a) }

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects
// (credential deletion, owner count adjustment) persist even though the tx
// sandbox is rolled back for tec results.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (a *AccountDelete) ApplyOnTec(ctx *tx.ApplyContext) {
	credential.RemoveExpiredCredentials(ctx, a.CredentialIDs)
}

func (a *AccountDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("account delete apply",
		"account", a.Account,
		"destination", a.Destination,
	)

	rules := ctx.Rules()
	if len(a.CredentialIDs) > 0 && !rules.Enabled(amendment.FeatureCredentials) {
		return tx.TemDISABLED
	}
	destAccount, destID, result := ctx.LookupAccount(a.Destination)
	if result != tx.TesSUCCESS {
		return result
	}
	destKey := keylet.Account(destID)
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && a.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}
	if len(a.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if result := credential.ValidateCredentialIDs(ctx, a.CredentialIDs); result != tx.TesSUCCESS {
			return result
		}
	}
	if len(a.CredentialIDs) == 0 {
		if rules.Enabled(amendment.FeatureDepositAuth) && (destAccount.Flags&state.LsfDepositAuth) != 0 {
			preauthKey := keylet.DepositPreauth(destID, ctx.AccountID)
			if exists, _ := ctx.View.Exists(preauthKey); !exists {
				return tx.TecNO_PERMISSION
			}
		}
	}
	// NFToken obligations check — must come BEFORE the sequence too-soon check
	// to match rippled's DeleteAccount::preclaim() order.
	if rules.Enabled(amendment.FeatureNonFungibleTokensV1) {
		if ctx.Account.MintedNFTokens != ctx.Account.BurnedNFTokens {
			return tx.TecHAS_OBLIGATIONS
		}
		first := keylet.NFTokenPageMin(ctx.AccountID)
		last := keylet.NFTokenPageMax(ctx.AccountID)
		succKey, _, succFound, succErr := ctx.View.Succ(first.Key)
		if succErr == nil && succFound && keyLessEqual(succKey, last.Key) {
			return tx.TecHAS_OBLIGATIONS
		}
	}
	// Check minimum ledger gap: account sequence must be far enough behind the ledger.
	// Uses addition (seq + 255 > ledgerSeq) instead of subtraction to avoid uint32 underflow.
	// Reference: rippled DeleteAccount.cpp preclaim():
	//   constexpr std::uint32_t seqDelta{255};
	//   if ((*sleAccount)[sfSequence] + seqDelta > ctx.view.seq())
	//       return tecTOO_SOON;
	//
	// Note: In rippled this check is in preclaim() before sequence consumption.
	// In our engine, Apply() runs after the sequence has already been incremented,
	// so we use the transaction's Sequence field (pre-increment value) for non-ticket
	// transactions, and ctx.Account.Sequence (unchanged) for ticket transactions.
	const seqDelta uint32 = 255
	acctSeq := ctx.Account.Sequence
	if a.GetCommon().TicketSequence == nil && a.GetCommon().Sequence != nil {
		acctSeq = *a.GetCommon().Sequence
	}
	if acctSeq+seqDelta > ctx.Config.LedgerSequence {
		return tx.TecTOO_SOON
	}
	if rules.Enabled(amendment.FeatureFixNFTokenRemint) {
		firstNFTSeq := uint32(0)
		if ctx.Account.HasFirstNFTSeq {
			firstNFTSeq = ctx.Account.FirstNFTokenSequence
		}
		if uint64(firstNFTSeq)+uint64(ctx.Account.MintedNFTokens)+uint64(seqDelta) > uint64(ctx.Config.LedgerSequence) {
			return tx.TecTOO_SOON
		}
	}
	// Verify deposit preauth with credentials BEFORE cleaning up owned objects.
	// Credentials in the owner directory will be deleted during cleanup, so this
	// check must happen first.
	// Reference: rippled DeleteAccount.cpp doApply() — verifyDepositPreauth
	// is called before cleanupOnAccountDelete.
	if rules.Enabled(amendment.FeatureDepositAuth) && len(a.CredentialIDs) > 0 {
		if r := credential.VerifyDepositPreauth(ctx, a.CredentialIDs, ctx.AccountID, destID, destAccount); r != tx.TesSUCCESS {
			return r
		}
	}
	const maxDeletableDirEntries = 1000
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	var entryKeys [][32]byte
	deletableCount := 0
	if err := state.DirForEach(ctx.View, ownerDirKey, func(itemKey [32]byte) error {
		entryKeys = append(entryKeys, itemKey)
		return nil
	}); err != nil {
		return tx.TefINTERNAL
	}
	for _, itemKey := range entryKeys {
		ik := keylet.Keylet{Key: itemKey}
		data, err := ctx.View.Read(ik)
		if err != nil || data == nil {
			return tx.TefBAD_LEDGER
		}
		etRaw, err := state.GetLedgerEntryType(data)
		if err != nil {
			return tx.TecHAS_OBLIGATIONS
		}
		et := entry.Type(etRaw)
		deleter := nonObligationDeleter(et)
		if deleter == nil {
			ctx.Log.Error("account delete: undeletable item in owner directory",
				"entryType", etRaw,
			)
			return tx.TecHAS_OBLIGATIONS
		}
		deletableCount++
		if deletableCount > maxDeletableDirEntries {
			return tx.TefTOO_BIG
		}
		if r := deleter(ctx, ownerDirKey, ik, data); r != tx.TesSUCCESS {
			return r
		}
	}
	if dirData, err := ctx.View.Read(ownerDirKey); err == nil && dirData != nil {
		ctx.View.Erase(ownerDirKey)
	}
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		ctx.Log.Error("account delete: failed to re-read destination account")
		return tx.TefINTERNAL
	}
	destAccount, err = state.ParseAccountRoot(destData)
	if err != nil {
		ctx.Log.Error("account delete: failed to parse destination account")
		return tx.TefINTERNAL
	}
	sourceBalance := ctx.Account.Balance
	destAccount.Balance += sourceBalance
	ctx.Account.Balance -= sourceBalance
	if sourceBalance > 0 && (destAccount.Flags&state.LsfPasswordSpent) != 0 {
		destAccount.Flags &^= state.LsfPasswordSpent
	}
	if r := ctx.UpdateAccountRoot(destID, destAccount); r != tx.TesSUCCESS {
		return r
	}
	if r := ctx.UpdateAccountRoot(ctx.AccountID, ctx.Account); r != tx.TesSUCCESS {
		return r
	}
	if err := ctx.View.Erase(keylet.Account(ctx.AccountID)); err != nil {
		return tx.TefINTERNAL
	}
	return tx.TesSUCCESS
}

// entryDeleter removes one account-owned object during AccountDelete cleanup.
// It removes the object from every directory it lives in, erases the SLE and
// adjusts owner counts. Every directory-removal, parse or erase failure is
// reported uniformly as tefBAD_LEDGER, matching rippled's per-type deleters
// (offerDelete, removeTicketFromLedger, removeNFTokenOfferFromLedger, ...),
// all of which propagate ledger corruption as tef.
// Reference: rippled DeleteAccount.cpp nonObligationDeleter and its deleters.
type entryDeleter func(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result

// nonObligationDeleter returns the deleter for a non-obligation ledger entry
// type, or nil if the type is an obligation that blocks account deletion.
// Reference: rippled DeleteAccount.cpp nonObligationDeleter().
func nonObligationDeleter(t entry.Type) entryDeleter {
	switch t {
	case entry.TypeOffer:
		return deleteOffer
	case entry.TypeSignerList:
		return deleteSignerList
	case entry.TypeTicket:
		return deleteTicket
	case entry.TypeDepositPreauth:
		return deleteDepositPreauth
	case entry.TypeNFTokenOffer:
		return deleteNFTokenOffer
	case entry.TypeDID:
		return deleteDID
	case entry.TypeOracle:
		return deleteOracle
	case entry.TypeCredential:
		return deleteCredential
	case entry.TypeDelegate:
		return deleteDelegate
	default:
		return nil
	}
}

// removeFromDir removes itemKey from the directory page hinted at by hint,
// reporting both lookup errors and a not-found item as failure — a directory
// entry that cannot be removed is ledger corruption.
func removeFromDir(ctx *tx.ApplyContext, dir keylet.Keylet, hint uint64, itemKey [32]byte, keepRoot bool) bool {
	res, err := state.DirRemove(ctx.View, dir, hint, itemKey, keepRoot)
	return err == nil && res.Success
}

func deleteOffer(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	offer, err := state.ParseLedgerOffer(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if !removeFromDir(ctx, ownerDirKey, offer.OwnerNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	bdk := keylet.Keylet{Type: 100, Key: offer.BookDirectory}
	if !removeFromDir(ctx, bdk, offer.BookNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	decrementOwnerCount(ctx)
	return tx.TesSUCCESS
}

func deleteTicket(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	if !removeFromDir(ctx, ownerDirKey, state.GetOwnerNode(data), ik.Key, true) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	decrementOwnerCount(ctx)
	if ctx.Account.TicketCount > 0 {
		ctx.Account.TicketCount--
	}
	return tx.TesSUCCESS
}

func deleteNFTokenOffer(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	nftOffer, err := state.ParseNFTokenOffer(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if !removeFromDir(ctx, ownerDirKey, nftOffer.OwnerNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	var tdk keylet.Keylet
	if nftOffer.Flags&entry.LsfSellNFToken != 0 {
		tdk = keylet.NFTSells(nftOffer.NFTokenID)
	} else {
		tdk = keylet.NFTBuys(nftOffer.NFTokenID)
	}
	if !removeFromDir(ctx, tdk, nftOffer.NFTokenOfferNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	decrementOwnerCount(ctx)
	return tx.TesSUCCESS
}

func deleteDepositPreauth(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	pe, err := state.ParseDepositPreauth(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if !removeFromDir(ctx, ownerDirKey, pe.OwnerNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	decrementOwnerCount(ctx)
	return tx.TesSUCCESS
}

func deleteDID(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	dd, err := state.ParseDID(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if !removeFromDir(ctx, ownerDirKey, dd.OwnerNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	decrementOwnerCount(ctx)
	return tx.TesSUCCESS
}

func deleteSignerList(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	signerList, err := state.ParseSignerList(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if !removeFromDir(ctx, ownerDirKey, signerList.OwnerNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	// A post-MultiSignReserve list (lsfOneOwnerCount) costs a single owner unit;
	// a legacy list costs 2 plus one per signer entry.
	removeCount := uint32(1)
	if signerList.Flags&state.LsfOneOwnerCount == 0 {
		removeCount = 2 + uint32(len(signerList.SignerEntries))
	}
	decrementOwnerCountBy(ctx, removeCount)
	return tx.TesSUCCESS
}

func deleteDelegate(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	dd, err := state.ParseDelegate(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if !removeFromDir(ctx, ownerDirKey, dd.OwnerNode, ik.Key, false) {
		return tx.TefBAD_LEDGER
	}
	if err := ctx.View.Erase(ik); err != nil {
		return tx.TefBAD_LEDGER
	}
	decrementOwnerCount(ctx)
	return tx.TesSUCCESS
}

// deleteCredential and deleteOracle delegate to helpers that own their full
// deletion (directory removal, owner-count adjustment through the view, and
// erase), so the deleter only maps a non-nil error to tefBAD_LEDGER.
func deleteCredential(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	cred, err := credential.ParseCredentialEntry(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if result := credential.DeleteSLE(ctx, ik, cred); result != tx.TesSUCCESS {
		return result
	}
	return tx.TesSUCCESS
}

func deleteOracle(ctx *tx.ApplyContext, ownerDirKey, ik keylet.Keylet, data []byte) tx.Result {
	od, err := state.ParseOracle(data)
	if err != nil {
		return tx.TefBAD_LEDGER
	}
	if r := oracle.DeleteOracleFromView(ctx.View, ik, od, ctx.AccountID, nil); r != tx.TesSUCCESS {
		return tx.TefBAD_LEDGER
	}
	return tx.TesSUCCESS
}

func decrementOwnerCount(ctx *tx.ApplyContext) {
	decrementOwnerCountBy(ctx, 1)
}

func decrementOwnerCountBy(ctx *tx.ApplyContext, n uint32) {
	if ctx.Account.OwnerCount >= n {
		ctx.Account.OwnerCount -= n
	} else {
		ctx.Account.OwnerCount = 0
	}
}

func keyLessEqual(a, b [32]byte) bool {
	for i := range 32 {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return true
}

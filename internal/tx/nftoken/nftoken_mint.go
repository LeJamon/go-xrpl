package nftoken

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// NFTokenMint mints a new NFToken.
type NFTokenMint struct {
	tx.BaseTx

	// NFTokenTaxon is the taxon for this token (required)
	NFTokenTaxon uint32 `json:"NFTokenTaxon" xrpl:"NFTokenTaxon"`

	// Issuer is the issuer of the token (optional, defaults to Account)
	Issuer string `json:"Issuer,omitempty" xrpl:"Issuer,omitempty"`

	// TransferFee is the fee for secondary sales (0-50000, where 50000 = 50%)
	TransferFee *uint16 `json:"TransferFee,omitempty" xrpl:"TransferFee,omitempty"`

	// URI is the URI for the token metadata (optional)
	URI string `json:"URI,omitempty" xrpl:"URI,omitempty"`

	// Amount is the minting price (optional)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`

	// Destination is the account to receive the minted token (optional)
	Destination string `json:"Destination,omitempty" xrpl:"Destination,omitempty"`

	// Expiration is when the mint offer expires (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`
}

// NFTokenMint flags
const (
	// tfBurnable allows the issuer to burn the token
	NFTokenMintFlagBurnable uint32 = 0x00000001
	// tfOnlyXRP allows only XRP for sale
	NFTokenMintFlagOnlyXRP uint32 = 0x00000002
	// tfTrustLine creates trust lines for transfer (deprecated by fixRemoveNFTokenAutoTrustLine)
	NFTokenMintFlagTrustLine uint32 = 0x00000004
	// tfTransferable allows the token to be transferred
	NFTokenMintFlagTransferable uint32 = 0x00000008
	// tfMutable allows the URI to be modified (requires DynamicNFT amendment)
	NFTokenMintFlagMutable uint32 = 0x00000010

	// Reference: rippled TxFlags.h tfNFTokenMintMask — all masks carve out
	// tfUniversal so inner Batch txs (which carry tfInnerBatchTxn) aren't rejected.
	// The four variants correspond to rippled's amendment-conditional selection in
	// NFTokenMint::getFlagsMask (fixRemoveNFTokenAutoTrustLine × DynamicNFT):
	// the "Old" masks permit tfTrustLine, the "WithMutable" masks permit tfMutable.
	tfNFTokenMintMask               uint32 = ^(tx.TfUniversal | NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTransferable)
	tfNFTokenMintMaskWithMutable    uint32 = ^(tx.TfUniversal | NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTransferable | NFTokenMintFlagMutable)
	tfNFTokenMintOldMask            uint32 = ^(tx.TfUniversal | NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTrustLine | NFTokenMintFlagTransferable)
	tfNFTokenMintOldMaskWithMutable uint32 = ^(tx.TfUniversal | NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTrustLine | NFTokenMintFlagTransferable | NFTokenMintFlagMutable)
)

// NewNFTokenMint creates a new NFTokenMint transaction
func NewNFTokenMint(account string, taxon uint32) *NFTokenMint {
	return &NFTokenMint{
		BaseTx:       *tx.NewBaseTx(tx.TypeNFTokenMint, account),
		NFTokenTaxon: taxon,
	}
}

func (n *NFTokenMint) TxType() tx.Type {
	return tx.TypeNFTokenMint
}

// GetFlagsMask returns the amendment-conditional invalid-flags mask, enforced by
// the engine at preflight0. tfTrustLine is only permitted before
// fixRemoveNFTokenAutoTrustLine; tfMutable is only permitted once DynamicNFT is
// enabled.
// Reference: rippled NFTokenMint::getFlagsMask.
func (n *NFTokenMint) GetFlagsMask(rules *amendment.Rules) uint32 {
	dynamicNFT := rules.NFTsWithDynamicEnabled()
	if rules.Enabled(amendment.FeatureFixRemoveNFTokenAutoTrustLine) {
		if dynamicNFT {
			return tfNFTokenMintMaskWithMutable
		}
		return tfNFTokenMintMask
	}
	if dynamicNFT {
		return tfNFTokenMintOldMaskWithMutable
	}
	return tfNFTokenMintOldMask
}

// Reference: rippled NFTokenMint.cpp preflight
func (n *NFTokenMint) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Flag mask is enforced by the engine at preflight0 via GetFlagsMask.

	// TransferFee must be <= maxTransferFee (50000 = 50%)
	if n.TransferFee != nil {
		if *n.TransferFee > maxTransferFee {
			return ter.Errorf(ter.TemBAD_NFTOKEN_TRANSFER_FEE, "TransferFee cannot exceed 50000")
		}
		// If a non-zero TransferFee is set, tfTransferable must also be set
		if *n.TransferFee > 0 && n.GetFlags()&NFTokenMintFlagTransferable == 0 {
			return ter.Errorf(ter.TemMALFORMED, "non-zero TransferFee requires tfTransferable flag")
		}
	}

	// Issuer must not be the same as Account (if specified)
	if n.Issuer != "" && n.Issuer == n.Account {
		return ter.Errorf(ter.TemMALFORMED, "Issuer cannot be the same as Account")
	}

	// URI validation: if the field is present, it must not be empty and must
	// not exceed maxTokenURILength bytes.
	// Reference: rippled NFTokenMint.cpp preflight — checks isFieldPresent(sfURI)
	// then rejects empty or oversized URIs.
	// HasField("URI") distinguishes binary-parsed "URI present but empty" from "URI absent".
	// For Go-created transactions (no PresentFields), fall back to n.URI != "".
	if n.HasField("URI") || n.URI != "" {
		uriBytes := len(n.URI) / 2
		if uriBytes == 0 {
			return ter.Errorf(ter.TemMALFORMED, "URI cannot be empty")
		}
		if uriBytes > maxTokenURILength {
			return ter.Errorf(ter.TemMALFORMED, "URI too long")
		}
	}

	// If Amount, Destination, or Expiration are present, Amount is required
	// (This is NFTokenMintOffer support)
	hasOfferFields := n.Amount != nil || n.Destination != "" || n.Expiration != nil
	if hasOfferFields && n.Amount == nil {
		return ter.Errorf(ter.TemMALFORMED, "Amount required when Destination or Expiration present")
	}

	// The Amount-dependent offer checks (negative, OnlyXRP, zero, expiration,
	// destination) run in PreflightRules because their order and gating depend
	// on the active amendments.

	return nil
}

// PreflightRules runs the amendment-aware offer validation shared with
// NFTokenCreateOffer, when Mint carries offer fields. A Mint always creates a
// sell offer with no Owner. This runs after Validate (rippled invokes
// tokenOfferCreatePreflight at the end of NFTokenMint::preflight, after the
// TransferFee/Issuer/URI/Amount-required checks).
// Reference: rippled NFTokenMint.cpp preflight → nft::tokenOfferCreatePreflight.
func (n *NFTokenMint) PreflightRules(rules *amendment.Rules) error {
	if n.Amount == nil {
		return nil
	}
	nftFlags := uint16(n.GetFlags() & 0xFFFF)
	return tokenOfferCreatePreflight(rules, n.Account, *n.Amount, n.Destination, n.Expiration, nftFlags, "", true)
}

func (n *NFTokenMint) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// SetBurnable makes the token burnable by the issuer
func (n *NFTokenMint) SetBurnable() {
	flags := n.GetFlags() | NFTokenMintFlagBurnable
	n.SetFlags(flags)
}

// SetTransferable makes the token transferable
func (n *NFTokenMint) SetTransferable() {
	flags := n.GetFlags() | NFTokenMintFlagTransferable
	n.SetFlags(flags)
}

// When offer fields (Amount, Destination, Expiration) are present, requires
// FeatureNFTokenMintOffer.
// Reference: rippled NFTokenMint.cpp preflight — temDISABLED when offer fields present without amendment
func (n *NFTokenMint) RequiredAmendments() [][32]byte {
	if n.Amount != nil || n.Destination != "" || n.Expiration != nil {
		return [][32]byte{amendment.FeatureNFTokenMintOffer}
	}
	return nil
}

// Reference: rippled NFTokenMint.cpp doApply
func (n *NFTokenMint) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("nftoken mint apply",
		"account", n.Account,
		"taxon", n.NFTokenTaxon,
		"transferFee", n.TransferFee,
		"flags", n.GetFlags(),
	)

	// The amendment-conditional flag mask is enforced by the engine at preflight0
	// via GetFlagsMask.

	accountID := ctx.AccountID

	// Record owner count before insertion for reserve check.
	// Reference: rippled NFTokenMint.cpp doApply line 296-297
	ownerCountBefore := ctx.Account.OwnerCount

	// mPriorBalance is the source balance before its own fee was deducted
	// (rippled's mPriorBalance), used for the reserve check below.
	mPriorBalance := ctx.PriorBalance()

	// Determine the issuer
	var issuerID [20]byte
	var issuerAccount *state.AccountRoot
	var issuerKey keylet.Keylet

	if n.Issuer != "" {
		var err error
		issuerID, err = state.DecodeAccountID(n.Issuer)
		if err != nil {
			return ter.TemINVALID
		}

		// Read issuer account for MintedNFTokens tracking
		issuerKey = keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil || issuerData == nil {
			return ter.TecNO_ISSUER
		}
		issuerAccount, err = state.ParseAccountRoot(issuerData)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Verify that Account is authorized to mint for this issuer
		// The issuer must have set Account as their NFTokenMinter
		if issuerAccount.NFTokenMinter != n.Account {
			ctx.Log.Warn("nftoken mint: account not authorized to mint for issuer",
				"issuer", n.Issuer,
			)
			return ter.TecNO_PERMISSION
		}
	} else {
		issuerID = accountID
		issuerAccount = ctx.Account
	}

	// Preclaim checks for the combined mint+offer path.
	// Reference: rippled NFTokenMint.cpp preclaim → tokenOfferCreatePreclaim
	if n.Amount != nil {
		// The negative-amount tem* check runs in preflight (PreflightRules →
		// tokenOfferCreatePreflight), before this point.

		if tx.HasExpired(n.Expiration, ctx.Config.ParentCloseTime) {
			return ter.TecEXPIRED
		}

		// Extract NFToken flags from transaction flags (lower 16 bits)
		// These are the flags that will be embedded in the minted token.
		nftFlags := uint16(n.GetFlags() & 0xFFFF)

		var transferFee uint16
		if n.TransferFee != nil {
			transferFee = *n.TransferFee
		}

		// IOU-specific preclaim checks
		// Reference: rippled NFTokenUtils.cpp tokenOfferCreatePreclaim
		if !n.Amount.IsNative() {
			iouIssuerID, err := state.DecodeAccountID(n.Amount.Issuer)
			if err != nil {
				return ter.TemINVALID
			}

			// NFT issuer trust line check when transfer fee is set and no auto-trust-line flag
			// Reference: rippled tokenOfferCreatePreclaim lines 909-929
			if nftFlags&NFTokenFlagTrustLine == 0 && transferFee > 0 {
				issuerExists, _ := ctx.View.Exists(keylet.Account(issuerID))
				if !issuerExists {
					return ter.TecNO_ISSUER
				}

				// With featureNFTokenMintOffer: skip trust line check when NFT issuer == IOU issuer
				if ctx.Rules().Enabled(amendment.FeatureNFTokenMintOffer) {
					if issuerID != iouIssuerID {
						trustLineKey := keylet.Line(issuerID, iouIssuerID, n.Amount.Currency)
						trustLineData, err := ctx.View.Read(trustLineKey)
						if err != nil || trustLineData == nil {
							return ter.TecNO_LINE
						}
					}
				} else {
					trustLineKey := keylet.Line(issuerID, iouIssuerID, n.Amount.Currency)
					exists, _ := ctx.View.Exists(trustLineKey)
					if !exists {
						return ter.TecNO_LINE
					}
				}

				if tx.IsGlobalFrozen(ctx.View, n.Amount.Issuer) || tx.IsTrustlineFrozen(ctx.View, issuerID, iouIssuerID, n.Amount.Currency) {
					return ter.TecFROZEN
				}
			}

			// Check if the minting account is frozen for this IOU
			// Reference: rippled tokenOfferCreatePreclaim line 941
			if tx.IsGlobalFrozen(ctx.View, n.Amount.Issuer) || tx.IsTrustlineFrozen(ctx.View, accountID, iouIssuerID, n.Amount.Currency) {
				return ter.TecFROZEN
			}

			// Trust line authorization check (with fixEnforceNFTokenTrustlineV2)
			// Reference: rippled tokenOfferCreatePreclaim lines 1007-1018
			if ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
				if r := checkNFTTrustlineAuthorized(ctx.View, accountID, n.Amount.Currency, iouIssuerID); r != ter.TesSUCCESS {
					return r
				}
			}
		}

		// Destination check
		// Reference: rippled tokenOfferCreatePreclaim lines 970-988
		if n.Destination != "" {
			destAccount, _, result := ctx.LookupAccount(n.Destination)
			if result != ter.TesSUCCESS {
				return result
			}
			if destAccount.Flags&state.LsfDisallowIncomingNFTokenOffer != 0 {
				return ter.TecNO_PERMISSION
			}
		}
	}

	// The token's unique sequence is FirstNFTokenSequence + MintedNFTokens.
	// Reference: rippled NFTokenMint.cpp doApply
	var tokenSeq uint32

	// If the issuer hasn't minted an NFToken before, set FirstNFTokenSequence.
	if !issuerAccount.HasFirstNFTSeq {
		acctSeq := issuerAccount.Sequence
		// If minted by authorized minter (Issuer field present) or using a ticket,
		// use acctSeq as-is. Otherwise, the sequence was pre-incremented, so use acctSeq - 1.
		if n.Issuer != "" || n.GetCommon().TicketSequence != nil {
			issuerAccount.FirstNFTokenSequence = acctSeq
		} else {
			issuerAccount.FirstNFTokenSequence = acctSeq - 1
		}
		issuerAccount.HasFirstNFTSeq = true
	}

	mintedNftCnt := issuerAccount.MintedNFTokens
	issuerAccount.MintedNFTokens = mintedNftCnt + 1
	if issuerAccount.MintedNFTokens == 0 {
		return ter.TecMAX_SEQUENCE_REACHED
	}

	// tokenSeq = FirstNFTokenSequence + MintedNFTokens (before increment)
	offset := issuerAccount.FirstNFTokenSequence
	tokenSeq = offset + mintedNftCnt

	if tokenSeq+1 == 0 || tokenSeq < offset {
		return ter.TecMAX_SEQUENCE_REACHED
	}

	// Get flags for the token from transaction flags
	txFlags := n.GetFlags()
	var tokenFlags uint16
	if txFlags&NFTokenMintFlagBurnable != 0 {
		tokenFlags |= NFTokenFlagBurnable
	}
	if txFlags&NFTokenMintFlagOnlyXRP != 0 {
		tokenFlags |= NFTokenFlagOnlyXRP
	}
	if txFlags&NFTokenMintFlagTrustLine != 0 {
		tokenFlags |= NFTokenFlagTrustLine
	}
	if txFlags&NFTokenMintFlagTransferable != 0 {
		tokenFlags |= NFTokenFlagTransferable
	}
	if txFlags&NFTokenMintFlagMutable != 0 {
		tokenFlags |= NFTokenFlagMutable
	}

	var transferFee uint16
	if n.TransferFee != nil {
		transferFee = *n.TransferFee
	}

	// Generate the NFTokenID
	tokenID := generateNFTokenID(issuerID, n.NFTokenTaxon, tokenSeq, tokenFlags, transferFee)

	// Insert the NFToken into the owner's token directory
	// Reference: rippled NFTokenUtils.cpp insertToken
	newToken := state.NFTokenData{
		NFTokenID: tokenID,
		URI:       n.URI,
	}

	fixDirV1 := ctx.Rules().Enabled(amendment.FeatureFixNFTokenDirV1)
	insertResult := insertNFToken(accountID, newToken, ctx.View, fixDirV1)
	if insertResult.Result != ter.TesSUCCESS {
		ctx.Log.Error("nftoken mint: failed to insert token", "result", insertResult.Result)
		return insertResult.Result
	}

	ctx.Account.OwnerCount += uint32(insertResult.PagesCreated)

	// MintedNFTokens was already incremented above.

	// If issuer is different from minter, update the issuer account - tracked automatically
	if n.Issuer != "" {
		issuerUpdatedData, err := state.SerializeAccountRoot(issuerAccount)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(issuerKey, issuerUpdatedData); err != nil {
			return ter.TefINTERNAL
		}
	}

	// If Amount field is present, create a sell offer for the newly minted token.
	// Reference: rippled NFTokenMint.cpp doApply — tokenOfferCreateApply
	if n.Amount != nil {
		seqProxy := n.GetCommon().SeqProxy()
		result := tokenOfferCreateApply(ctx, accountID, tokenID, n.Amount, n.Destination, n.Expiration, seqProxy, mPriorBalance)
		if result != ter.TesSUCCESS {
			return result
		}
	}

	// Only check the reserve if the owner count actually changed. This
	// allows NFTs to be added to the page (and burn fees) without
	// requiring the reserve to be met each time. The reserve is
	// only managed when a new NFT page or sell offer is added.
	// Reference: rippled NFTokenMint.cpp doApply lines 350-357
	if ctx.Account.OwnerCount > ownerCountBefore {
		reserve := ctx.AccountReserve(ctx.Account.OwnerCount)
		if mPriorBalance < reserve {
			return ter.TecINSUFFICIENT_RESERVE
		}
	}

	return ter.TesSUCCESS
}

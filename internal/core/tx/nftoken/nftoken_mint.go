package nftoken

import (
	"errors"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeNFTokenMint, func() tx.Transaction {
		return &NFTokenMint{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenMint, "")}
	})
}

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

	// tfNFTokenMintMask is the mask for valid flags (with fixRemoveNFTokenAutoTrustLine)
	tfNFTokenMintMask uint32 = ^(NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTransferable)
	// tfNFTokenMintMaskWithMutable includes mutable flag
	tfNFTokenMintMaskWithMutable uint32 = ^(NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTransferable | NFTokenMintFlagMutable)
	// tfNFTokenMintOldMask is the mask for valid flags (before fixRemoveNFTokenAutoTrustLine)
	tfNFTokenMintOldMask uint32 = ^(NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTrustLine | NFTokenMintFlagTransferable)
	// tfNFTokenMintOldMaskWithMutable includes mutable flag
	tfNFTokenMintOldMaskWithMutable uint32 = ^(NFTokenMintFlagBurnable | NFTokenMintFlagOnlyXRP | NFTokenMintFlagTrustLine | NFTokenMintFlagTransferable | NFTokenMintFlagMutable)
)

// NewNFTokenMint creates a new NFTokenMint transaction
func NewNFTokenMint(account string, taxon uint32) *NFTokenMint {
	return &NFTokenMint{
		BaseTx:       *tx.NewBaseTx(tx.TypeNFTokenMint, account),
		NFTokenTaxon: taxon,
	}
}

// TxType returns the transaction type
func (n *NFTokenMint) TxType() tx.Type {
	return tx.TypeNFTokenMint
}

// Validate validates the NFTokenMint transaction
// Reference: rippled NFTokenMint.cpp preflight
func (n *NFTokenMint) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Use the old (permissive) mask here since Validate() has no access to Rules.
	// The amendment-dependent check (rejecting tfTrustLine when
	// fixRemoveNFTokenAutoTrustLine is enabled) is in Apply().
	if n.GetFlags()&tfNFTokenMintOldMask != 0 {
		return errors.New("temINVALID_FLAG: invalid NFTokenMint flags")
	}

	// TransferFee must be <= maxTransferFee (50000 = 50%)
	if n.TransferFee != nil {
		if *n.TransferFee > maxTransferFee {
			return errors.New("temBAD_NFTOKEN_TRANSFER_FEE: TransferFee cannot exceed 50000")
		}
		// If a non-zero TransferFee is set, tfTransferable must also be set
		if *n.TransferFee > 0 && n.GetFlags()&NFTokenMintFlagTransferable == 0 {
			return errors.New("temMALFORMED: non-zero TransferFee requires tfTransferable flag")
		}
	}

	// Issuer must not be the same as Account (if specified)
	if n.Issuer != "" && n.Issuer == n.Account {
		return errors.New("temMALFORMED: Issuer cannot be the same as Account")
	}

	// URI validation: must be hex-encoded, not empty (if present), and <= maxTokenURILength bytes
	if n.URI != "" {
		// URI is hex-encoded, so length in bytes is len/2
		uriBytes := len(n.URI) / 2
		if uriBytes == 0 {
			return errors.New("temMALFORMED: URI cannot be empty")
		}
		if uriBytes > maxTokenURILength {
			return errors.New("temMALFORMED: URI too long")
		}
	}

	// If Amount, Destination, or Expiration are present, Amount is required
	// (This is NFTokenMintOffer support)
	hasOfferFields := n.Amount != nil || n.Destination != "" || n.Expiration != nil
	if hasOfferFields && n.Amount == nil {
		return errors.New("temMALFORMED: Amount required when Destination or Expiration present")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
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

// RequiredAmendments returns the amendments required for this transaction type.
// When offer fields (Amount, Destination, Expiration) are present, also requires
// FeatureNFTokenMintOffer.
// Reference: rippled NFTokenMint.cpp preflight — temDISABLED when offer fields present without amendment
func (n *NFTokenMint) RequiredAmendments() [][32]byte {
	amends := [][32]byte{amendment.FeatureNonFungibleTokensV1}
	if n.Amount != nil || n.Destination != "" || n.Expiration != nil {
		amends = append(amends, amendment.FeatureNFTokenMintOffer)
	}
	return amends
}

// Apply applies the NFTokenMint transaction to the ledger.
// Reference: rippled NFTokenMint.cpp doApply
func (m *NFTokenMint) Apply(ctx *tx.ApplyContext) tx.Result {
	// Amendment-dependent flag check: with fixRemoveNFTokenAutoTrustLine,
	// tfTrustLine is no longer valid.
	// Reference: rippled NFTokenMint.cpp preflight — mask depends on amendment
	if ctx.Rules().Enabled(amendment.FeatureFixRemoveNFTokenAutoTrustLine) {
		if m.GetFlags()&tfNFTokenMintMask != 0 {
			return tx.TemINVALID_FLAG
		}
	}

	accountID := ctx.AccountID

	// Determine the issuer
	var issuerID [20]byte
	var issuerAccount *sle.AccountRoot
	var issuerKey keylet.Keylet

	if m.Issuer != "" {
		var err error
		issuerID, err = sle.DecodeAccountID(m.Issuer)
		if err != nil {
			return tx.TemINVALID
		}

		// Read issuer account for MintedNFTokens tracking
		issuerKey = keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil || issuerData == nil {
			return tx.TecNO_ISSUER
		}
		issuerAccount, err = sle.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Verify that Account is authorized to mint for this issuer
		// The issuer must have set Account as their NFTokenMinter
		if issuerAccount.NFTokenMinter != m.Account {
			return tx.TecNO_PERMISSION
		}
	} else {
		issuerID = accountID
		issuerAccount = ctx.Account
	}

	// Get the token sequence from MintedNFTokens
	tokenSeq := issuerAccount.MintedNFTokens

	// Check for overflow
	if tokenSeq+1 < tokenSeq {
		return tx.TecMAX_SEQUENCE_REACHED
	}

	// Get flags for the token from transaction flags
	txFlags := m.GetFlags()
	var tokenFlags uint16
	if txFlags&NFTokenMintFlagBurnable != 0 {
		tokenFlags |= nftFlagBurnable
	}
	if txFlags&NFTokenMintFlagOnlyXRP != 0 {
		tokenFlags |= nftFlagOnlyXRP
	}
	if txFlags&NFTokenMintFlagTrustLine != 0 {
		tokenFlags |= nftFlagTrustLine
	}
	if txFlags&NFTokenMintFlagTransferable != 0 {
		tokenFlags |= nftFlagTransferable
	}
	if txFlags&NFTokenMintFlagMutable != 0 {
		tokenFlags |= nftFlagMutable
	}

	// Get transfer fee
	var transferFee uint16
	if m.TransferFee != nil {
		transferFee = *m.TransferFee
	}

	// Generate the NFTokenID
	tokenID := generateNFTokenID(issuerID, m.NFTokenTaxon, tokenSeq, tokenFlags, transferFee)

	// Insert the NFToken into the owner's token directory
	// Reference: rippled NFTokenUtils.cpp insertToken
	newToken := sle.NFTokenData{
		NFTokenID: tokenID,
		URI:       m.URI,
	}

	insertResult := insertNFToken(accountID, newToken, ctx.View)
	if insertResult.Result != tx.TesSUCCESS {
		return insertResult.Result
	}

	// Update owner count based on pages created
	ctx.Account.OwnerCount += uint32(insertResult.PagesCreated)

	// Update MintedNFTokens on the issuer account
	issuerAccount.MintedNFTokens = tokenSeq + 1

	// If issuer is different from minter, update the issuer account - tracked automatically
	if m.Issuer != "" {
		issuerUpdatedData, err := sle.SerializeAccountRoot(issuerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(issuerKey, issuerUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// If Amount field is present, create a sell offer for the newly minted token.
	// Reference: rippled NFTokenMint.cpp doApply — tokenOfferCreateApply
	if m.Amount != nil {
		seqProxy := m.GetCommon().SeqProxy()
		result := tokenOfferCreateApply(ctx, accountID, tokenID, m.Amount, m.Destination, m.Expiration, seqProxy)
		if result != tx.TesSUCCESS {
			return result
		}
	}

	// Check reserve for all new objects (pages + possible offer)
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	return tx.TesSUCCESS
}

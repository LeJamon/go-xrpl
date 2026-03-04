package nft

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/ledgerstatefix"
	"github.com/LeJamon/goXRPLd/internal/core/tx/nftoken"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// NFTokenMintBuilder provides a fluent interface for building NFTokenMint transactions.
type NFTokenMintBuilder struct {
	account     *testing.Account
	taxon       uint32
	issuer      *testing.Account
	transferFee *uint16
	uri         string
	amount      *tx.Amount
	destination *testing.Account
	expiration  *uint32
	fee         uint64
	sequence    *uint32
	flags       uint32
}

// NFTokenMint creates a new NFTokenMintBuilder.
// taxon is the taxon for categorizing the NFT.
func NFTokenMint(account *testing.Account, taxon uint32) *NFTokenMintBuilder {
	return &NFTokenMintBuilder{
		account: account,
		taxon:   taxon,
		fee:     10, // Default fee: 10 drops
	}
}

// Issuer sets the issuer of the token (for minting on behalf of another account).
func (b *NFTokenMintBuilder) Issuer(issuer *testing.Account) *NFTokenMintBuilder {
	b.issuer = issuer
	return b
}

// TransferFee sets the transfer fee for secondary sales (0-50000, where 50000 = 50%).
func (b *NFTokenMintBuilder) TransferFee(fee uint16) *NFTokenMintBuilder {
	b.transferFee = &fee
	return b
}

// URI sets the URI for the token metadata.
// The URI will be hex-encoded when building the transaction.
func (b *NFTokenMintBuilder) URI(uri string) *NFTokenMintBuilder {
	b.uri = uri
	return b
}

// URIHex sets the URI from an already hex-encoded string.
func (b *NFTokenMintBuilder) URIHex(uriHex string) *NFTokenMintBuilder {
	b.uri = uriHex
	return b
}

// Amount sets the minting price (for NFTokenMintOffer support).
func (b *NFTokenMintBuilder) Amount(amount tx.Amount) *NFTokenMintBuilder {
	b.amount = &amount
	return b
}

// Destination sets the account to receive the minted token.
func (b *NFTokenMintBuilder) Destination(dest *testing.Account) *NFTokenMintBuilder {
	b.destination = dest
	return b
}

// Expiration sets when the mint offer expires (in Ripple epoch seconds).
func (b *NFTokenMintBuilder) Expiration(exp uint32) *NFTokenMintBuilder {
	b.expiration = &exp
	return b
}

// Fee sets the transaction fee in drops.
func (b *NFTokenMintBuilder) Fee(f uint64) *NFTokenMintBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *NFTokenMintBuilder) Sequence(seq uint32) *NFTokenMintBuilder {
	b.sequence = &seq
	return b
}

// Burnable makes the token burnable by the issuer.
func (b *NFTokenMintBuilder) Burnable() *NFTokenMintBuilder {
	b.flags |= nftoken.NFTokenMintFlagBurnable
	return b
}

// OnlyXRP restricts the token to only be sold for XRP.
func (b *NFTokenMintBuilder) OnlyXRP() *NFTokenMintBuilder {
	b.flags |= nftoken.NFTokenMintFlagOnlyXRP
	return b
}

// Transferable makes the token transferable by non-issuers.
func (b *NFTokenMintBuilder) Transferable() *NFTokenMintBuilder {
	b.flags |= nftoken.NFTokenMintFlagTransferable
	return b
}

// TrustLine enables auto-trust line creation for the NFT.
// When set, the NFT issuer can receive IOU transfer fees even without a pre-existing trust line.
// Requires fixRemoveNFTokenAutoTrustLine to be DISABLED.
func (b *NFTokenMintBuilder) TrustLine() *NFTokenMintBuilder {
	b.flags |= nftoken.NFTokenMintFlagTrustLine
	return b
}

// Mutable makes the token's URI modifiable (requires DynamicNFT amendment).
func (b *NFTokenMintBuilder) Mutable() *NFTokenMintBuilder {
	b.flags |= nftoken.NFTokenMintFlagMutable
	return b
}

// Build constructs the NFTokenMint transaction.
func (b *NFTokenMintBuilder) Build() tx.Transaction {
	n := nftoken.NewNFTokenMint(b.account.Address, b.taxon)
	n.Fee = fmt.Sprintf("%d", b.fee)

	if b.issuer != nil {
		n.Issuer = b.issuer.Address
	}
	if b.transferFee != nil {
		n.TransferFee = b.transferFee
	}
	if b.uri != "" {
		// If URI is not already hex-encoded (doesn't look like hex), encode it
		if !isHexEncoded(b.uri) {
			n.URI = hex.EncodeToString([]byte(b.uri))
		} else {
			n.URI = b.uri
		}
	}
	if b.amount != nil {
		n.Amount = b.amount
	}
	if b.destination != nil {
		n.Destination = b.destination.Address
	}
	if b.expiration != nil {
		n.Expiration = b.expiration
	}
	if b.sequence != nil {
		n.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		n.SetFlags(b.flags)
	}

	return n
}

// BuildNFTokenMint is a convenience method that returns the concrete *nftoken.NFTokenMint type.
func (b *NFTokenMintBuilder) BuildNFTokenMint() *nftoken.NFTokenMint {
	return b.Build().(*nftoken.NFTokenMint)
}

// NFTokenBurnBuilder provides a fluent interface for building NFTokenBurn transactions.
type NFTokenBurnBuilder struct {
	account   *testing.Account
	nftokenID string
	owner     *testing.Account
	fee       uint64
	sequence  *uint32
}

// NFTokenBurn creates a new NFTokenBurnBuilder.
// nftokenID is the ID of the token to burn.
func NFTokenBurn(account *testing.Account, nftokenID string) *NFTokenBurnBuilder {
	return &NFTokenBurnBuilder{
		account:   account,
		nftokenID: nftokenID,
		fee:       10, // Default fee: 10 drops
	}
}

// Owner sets the owner of the token (for authorized burns by issuer).
func (b *NFTokenBurnBuilder) Owner(owner *testing.Account) *NFTokenBurnBuilder {
	b.owner = owner
	return b
}

// Fee sets the transaction fee in drops.
func (b *NFTokenBurnBuilder) Fee(f uint64) *NFTokenBurnBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *NFTokenBurnBuilder) Sequence(seq uint32) *NFTokenBurnBuilder {
	b.sequence = &seq
	return b
}

// Build constructs the NFTokenBurn transaction.
func (b *NFTokenBurnBuilder) Build() tx.Transaction {
	n := nftoken.NewNFTokenBurn(b.account.Address, b.nftokenID)
	n.Fee = fmt.Sprintf("%d", b.fee)

	if b.owner != nil {
		n.Owner = b.owner.Address
	}
	if b.sequence != nil {
		n.SetSequence(*b.sequence)
	}

	return n
}

// BuildNFTokenBurn is a convenience method that returns the concrete *nftoken.NFTokenBurn type.
func (b *NFTokenBurnBuilder) BuildNFTokenBurn() *nftoken.NFTokenBurn {
	return b.Build().(*nftoken.NFTokenBurn)
}

// NFTokenCreateOfferBuilder provides a fluent interface for building NFTokenCreateOffer transactions.
type NFTokenCreateOfferBuilder struct {
	account     *testing.Account
	nftokenID   string
	amount      tx.Amount
	owner       *testing.Account
	destination *testing.Account
	expiration  *uint32
	fee         uint64
	sequence    *uint32
	flags       uint32
}

// NFTokenCreateSellOffer creates a new NFTokenCreateOfferBuilder for a sell offer.
// The account is selling the NFT for the specified amount.
func NFTokenCreateSellOffer(account *testing.Account, nftokenID string, amount tx.Amount) *NFTokenCreateOfferBuilder {
	return &NFTokenCreateOfferBuilder{
		account:   account,
		nftokenID: nftokenID,
		amount:    amount,
		fee:       10, // Default fee: 10 drops
		flags:     nftoken.NFTokenCreateOfferFlagSellNFToken,
	}
}

// NFTokenCreateBuyOffer creates a new NFTokenCreateOfferBuilder for a buy offer.
// The account is offering to buy the NFT from owner for the specified amount.
func NFTokenCreateBuyOffer(account *testing.Account, nftokenID string, amount tx.Amount, owner *testing.Account) *NFTokenCreateOfferBuilder {
	return &NFTokenCreateOfferBuilder{
		account:   account,
		nftokenID: nftokenID,
		amount:    amount,
		owner:     owner,
		fee:       10, // Default fee: 10 drops
	}
}

// Destination sets who can accept this offer.
func (b *NFTokenCreateOfferBuilder) Destination(dest *testing.Account) *NFTokenCreateOfferBuilder {
	b.destination = dest
	return b
}

// Expiration sets when the offer expires (in Ripple epoch seconds).
func (b *NFTokenCreateOfferBuilder) Expiration(exp uint32) *NFTokenCreateOfferBuilder {
	b.expiration = &exp
	return b
}

// Fee sets the transaction fee in drops.
func (b *NFTokenCreateOfferBuilder) Fee(f uint64) *NFTokenCreateOfferBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *NFTokenCreateOfferBuilder) Sequence(seq uint32) *NFTokenCreateOfferBuilder {
	b.sequence = &seq
	return b
}

// Build constructs the NFTokenCreateOffer transaction.
func (b *NFTokenCreateOfferBuilder) Build() tx.Transaction {
	n := nftoken.NewNFTokenCreateOffer(b.account.Address, b.nftokenID, b.amount)
	n.Fee = fmt.Sprintf("%d", b.fee)

	if b.owner != nil {
		n.Owner = b.owner.Address
	}
	if b.destination != nil {
		n.Destination = b.destination.Address
	}
	if b.expiration != nil {
		n.Expiration = b.expiration
	}
	if b.sequence != nil {
		n.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		n.SetFlags(b.flags)
	}

	return n
}

// BuildNFTokenCreateOffer is a convenience method that returns the concrete *nftoken.NFTokenCreateOffer type.
func (b *NFTokenCreateOfferBuilder) BuildNFTokenCreateOffer() *nftoken.NFTokenCreateOffer {
	return b.Build().(*nftoken.NFTokenCreateOffer)
}

// NFTokenCancelOfferBuilder provides a fluent interface for building NFTokenCancelOffer transactions.
type NFTokenCancelOfferBuilder struct {
	account  *testing.Account
	offerIDs []string
	fee      uint64
	sequence *uint32
}

// NFTokenCancelOffer creates a new NFTokenCancelOfferBuilder.
// offerIDs is the list of offer IDs to cancel.
func NFTokenCancelOffer(account *testing.Account, offerIDs ...string) *NFTokenCancelOfferBuilder {
	return &NFTokenCancelOfferBuilder{
		account:  account,
		offerIDs: offerIDs,
		fee:      10, // Default fee: 10 drops
	}
}

// AddOffer adds an offer ID to the list of offers to cancel.
func (b *NFTokenCancelOfferBuilder) AddOffer(offerID string) *NFTokenCancelOfferBuilder {
	b.offerIDs = append(b.offerIDs, offerID)
	return b
}

// Fee sets the transaction fee in drops.
func (b *NFTokenCancelOfferBuilder) Fee(f uint64) *NFTokenCancelOfferBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *NFTokenCancelOfferBuilder) Sequence(seq uint32) *NFTokenCancelOfferBuilder {
	b.sequence = &seq
	return b
}

// Build constructs the NFTokenCancelOffer transaction.
func (b *NFTokenCancelOfferBuilder) Build() tx.Transaction {
	n := nftoken.NewNFTokenCancelOffer(b.account.Address, b.offerIDs)
	n.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		n.SetSequence(*b.sequence)
	}

	return n
}

// BuildNFTokenCancelOffer is a convenience method that returns the concrete *nftoken.NFTokenCancelOffer type.
func (b *NFTokenCancelOfferBuilder) BuildNFTokenCancelOffer() *nftoken.NFTokenCancelOffer {
	return b.Build().(*nftoken.NFTokenCancelOffer)
}

// NFTokenAcceptOfferBuilder provides a fluent interface for building NFTokenAcceptOffer transactions.
type NFTokenAcceptOfferBuilder struct {
	account    *testing.Account
	sellOffer  string
	buyOffer   string
	brokerFee  *tx.Amount
	fee        uint64
	sequence   *uint32
}

// NFTokenAcceptOffer creates a new NFTokenAcceptOfferBuilder.
func NFTokenAcceptOffer(account *testing.Account) *NFTokenAcceptOfferBuilder {
	return &NFTokenAcceptOfferBuilder{
		account: account,
		fee:     10, // Default fee: 10 drops
	}
}

// NFTokenAcceptSellOffer creates a builder to accept a sell offer.
func NFTokenAcceptSellOffer(account *testing.Account, sellOfferID string) *NFTokenAcceptOfferBuilder {
	return &NFTokenAcceptOfferBuilder{
		account:   account,
		sellOffer: sellOfferID,
		fee:       10, // Default fee: 10 drops
	}
}

// NFTokenAcceptBuyOffer creates a builder to accept a buy offer.
func NFTokenAcceptBuyOffer(account *testing.Account, buyOfferID string) *NFTokenAcceptOfferBuilder {
	return &NFTokenAcceptOfferBuilder{
		account:  account,
		buyOffer: buyOfferID,
		fee:      10, // Default fee: 10 drops
	}
}

// NFTokenBrokeredSale creates a builder for a brokered sale (matching buy and sell offers).
func NFTokenBrokeredSale(broker *testing.Account, sellOfferID, buyOfferID string) *NFTokenAcceptOfferBuilder {
	return &NFTokenAcceptOfferBuilder{
		account:   broker,
		sellOffer: sellOfferID,
		buyOffer:  buyOfferID,
		fee:       10, // Default fee: 10 drops
	}
}

// SellOffer sets the sell offer to accept.
func (b *NFTokenAcceptOfferBuilder) SellOffer(offerID string) *NFTokenAcceptOfferBuilder {
	b.sellOffer = offerID
	return b
}

// BuyOffer sets the buy offer to accept.
func (b *NFTokenAcceptOfferBuilder) BuyOffer(offerID string) *NFTokenAcceptOfferBuilder {
	b.buyOffer = offerID
	return b
}

// BrokerFee sets the broker fee for brokered sales.
func (b *NFTokenAcceptOfferBuilder) BrokerFee(amount tx.Amount) *NFTokenAcceptOfferBuilder {
	b.brokerFee = &amount
	return b
}

// Fee sets the transaction fee in drops.
func (b *NFTokenAcceptOfferBuilder) Fee(f uint64) *NFTokenAcceptOfferBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *NFTokenAcceptOfferBuilder) Sequence(seq uint32) *NFTokenAcceptOfferBuilder {
	b.sequence = &seq
	return b
}

// Build constructs the NFTokenAcceptOffer transaction.
func (b *NFTokenAcceptOfferBuilder) Build() tx.Transaction {
	n := nftoken.NewNFTokenAcceptOffer(b.account.Address)
	n.Fee = fmt.Sprintf("%d", b.fee)

	if b.sellOffer != "" {
		n.NFTokenSellOffer = b.sellOffer
	}
	if b.buyOffer != "" {
		n.NFTokenBuyOffer = b.buyOffer
	}
	if b.brokerFee != nil {
		n.NFTokenBrokerFee = b.brokerFee
	}
	if b.sequence != nil {
		n.SetSequence(*b.sequence)
	}

	return n
}

// BuildNFTokenAcceptOffer is a convenience method that returns the concrete *nftoken.NFTokenAcceptOffer type.
func (b *NFTokenAcceptOfferBuilder) BuildNFTokenAcceptOffer() *nftoken.NFTokenAcceptOffer {
	return b.Build().(*nftoken.NFTokenAcceptOffer)
}

// NFTokenModifyBuilder provides a fluent interface for building NFTokenModify transactions.
type NFTokenModifyBuilder struct {
	account   *testing.Account
	nftokenID string
	owner     *testing.Account
	uri       string
	fee       uint64
	sequence  *uint32
}

// NFTokenModify creates a new NFTokenModifyBuilder.
// nftokenID is the ID of the token to modify.
func NFTokenModify(account *testing.Account, nftokenID string) *NFTokenModifyBuilder {
	return &NFTokenModifyBuilder{
		account:   account,
		nftokenID: nftokenID,
		fee:       10, // Default fee: 10 drops
	}
}

// Owner sets the owner of the token (for modifying on behalf of the owner).
func (b *NFTokenModifyBuilder) Owner(owner *testing.Account) *NFTokenModifyBuilder {
	b.owner = owner
	return b
}

// URI sets the new URI for the token.
// The URI will be hex-encoded when building the transaction.
func (b *NFTokenModifyBuilder) URI(uri string) *NFTokenModifyBuilder {
	b.uri = uri
	return b
}

// URIHex sets the URI from an already hex-encoded string.
func (b *NFTokenModifyBuilder) URIHex(uriHex string) *NFTokenModifyBuilder {
	b.uri = uriHex
	return b
}

// Fee sets the transaction fee in drops.
func (b *NFTokenModifyBuilder) Fee(f uint64) *NFTokenModifyBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *NFTokenModifyBuilder) Sequence(seq uint32) *NFTokenModifyBuilder {
	b.sequence = &seq
	return b
}

// Build constructs the NFTokenModify transaction.
func (b *NFTokenModifyBuilder) Build() tx.Transaction {
	n := nftoken.NewNFTokenModify(b.account.Address, b.nftokenID)
	n.Fee = fmt.Sprintf("%d", b.fee)

	if b.owner != nil {
		n.Owner = b.owner.Address
	}
	if b.uri != "" {
		// If URI is not already hex-encoded, encode it
		if !isHexEncoded(b.uri) {
			n.URI = hex.EncodeToString([]byte(b.uri))
		} else {
			n.URI = b.uri
		}
	}
	if b.sequence != nil {
		n.SetSequence(*b.sequence)
	}

	return n
}

// BuildNFTokenModify is a convenience method that returns the concrete *nftoken.NFTokenModify type.
func (b *NFTokenModifyBuilder) BuildNFTokenModify() *nftoken.NFTokenModify {
	return b.Build().(*nftoken.NFTokenModify)
}

// LedgerStateFixBuilder provides a fluent interface for building LedgerStateFix transactions.
// Reference: rippled ledgerStateFix::nftPageLinks()
type LedgerStateFixBuilder struct {
	account  *testing.Account
	owner    *testing.Account
	fixType  *uint8
	fee      uint64
	sequence *uint32
	flags    uint32
}

// LedgerStateFixNFTPageLinks creates a new LedgerStateFixBuilder for NFToken page link repair.
// account submits the fix; owner is the account whose pages are repaired.
func LedgerStateFixNFTPageLinks(account, owner *testing.Account) *LedgerStateFixBuilder {
	fixType := uint8(1) // LedgerFixTypeNFTokenPageLink
	return &LedgerStateFixBuilder{
		account: account,
		owner:   owner,
		fixType: &fixType,
		fee:     10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *LedgerStateFixBuilder) Fee(f uint64) *LedgerStateFixBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *LedgerStateFixBuilder) Sequence(seq uint32) *LedgerStateFixBuilder {
	b.sequence = &seq
	return b
}

// Flags sets explicit transaction flags.
func (b *LedgerStateFixBuilder) Flags(flags uint32) *LedgerStateFixBuilder {
	b.flags = flags
	return b
}

// FixType overrides the LedgerFixType field.
func (b *LedgerStateFixBuilder) FixType(ft uint8) *LedgerStateFixBuilder {
	b.fixType = &ft
	return b
}

// NoOwner removes the Owner field from the transaction.
func (b *LedgerStateFixBuilder) NoOwner() *LedgerStateFixBuilder {
	b.owner = nil
	return b
}

// Build constructs the LedgerStateFix transaction.
func (b *LedgerStateFixBuilder) Build() tx.Transaction {
	l := ledgerstatefix.NewLedgerStateFix(b.account.Address, *b.fixType)
	l.Fee = fmt.Sprintf("%d", b.fee)

	if b.owner != nil {
		l.Owner = b.owner.Address
	}
	if b.sequence != nil {
		l.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		l.SetFlags(b.flags)
	}

	return l
}

// isHexEncoded checks if a string appears to be hex-encoded.
// Returns true if the string has even length and contains only hex characters.
func isHexEncoded(s string) bool {
	if len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// GetNextNFTokenID predicts the next NFT ID that will be generated when minting.
// Must be called BEFORE submitting the NFTokenMint transaction.
// Reference: rippled's token::getNextID(env, issuer, taxon, flags, xferFee).
func GetNextNFTokenID(env *testing.TestEnv, issuer *testing.Account, taxon uint32, flags uint16, transferFee uint16) string {
	// Get the current MintedNFTokens count (this will be the sequence for the next mint)
	tokenSeq := env.MintedCount(issuer)
	tokenID := nftoken.GenerateNFTokenID(issuer.ID, taxon, tokenSeq, flags, transferFee)
	return hex.EncodeToString(tokenID[:])
}

// GetOfferIndex predicts the offer index (keylet) that will be created.
// Must be called BEFORE submitting the NFTokenCreateOffer transaction.
// Reference: rippled's keylet::nftoffer(account, env.seq(account)).key.
func GetOfferIndex(env *testing.TestEnv, acc *testing.Account) string {
	seq := env.Seq(acc)
	k := keylet.NFTokenOffer(acc.ID, seq)
	return hex.EncodeToString(k.Key[:])
}

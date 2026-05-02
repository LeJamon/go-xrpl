package nftoken

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

func init() {
	if err := tx.Register(tx.TypeNFTokenAcceptOffer, func() tx.Transaction {
		return &NFTokenAcceptOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenAcceptOffer, "")}
	}); err != nil {
		panic(err)
	}
}

// NFTokenAcceptOffer accepts an NFToken offer.
type NFTokenAcceptOffer struct {
	tx.BaseTx

	// NFTokenSellOffer is the sell offer to accept (optional)
	NFTokenSellOffer string `json:"NFTokenSellOffer,omitempty" xrpl:"NFTokenSellOffer,omitempty"`

	// NFTokenBuyOffer is the buy offer to accept (optional)
	NFTokenBuyOffer string `json:"NFTokenBuyOffer,omitempty" xrpl:"NFTokenBuyOffer,omitempty"`

	// NFTokenBrokerFee is the broker fee for brokered sales (optional)
	NFTokenBrokerFee *tx.Amount `json:"NFTokenBrokerFee,omitempty" xrpl:"NFTokenBrokerFee,omitempty,amount"`
}

// NewNFTokenAcceptOffer creates a new NFTokenAcceptOffer transaction
func NewNFTokenAcceptOffer(account string) *NFTokenAcceptOffer {
	return &NFTokenAcceptOffer{
		BaseTx: *tx.NewBaseTx(tx.TypeNFTokenAcceptOffer, account),
	}
}

func (n *NFTokenAcceptOffer) TxType() tx.Type {
	return tx.TypeNFTokenAcceptOffer
}

// Reference: rippled NFTokenAcceptOffer.cpp preflight
func (n *NFTokenAcceptOffer) Validate() error {
	if err := n.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (no flags are valid for NFTokenAcceptOffer)
	if n.GetFlags() != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "NFTokenAcceptOffer does not accept any flags")
	}

	// Must have at least one offer
	if n.NFTokenSellOffer == "" && n.NFTokenBuyOffer == "" {
		return tx.Errorf(tx.TemMALFORMED, "must specify NFTokenSellOffer or NFTokenBuyOffer")
	}

	// BrokerFee only valid for brokered mode (both offers)
	if n.NFTokenBrokerFee != nil {
		if n.NFTokenSellOffer == "" || n.NFTokenBuyOffer == "" {
			return tx.Errorf(tx.TemMALFORMED, "NFTokenBrokerFee requires both sell and buy offers")
		}
		// BrokerFee must be positive (greater than zero)
		// Reference: rippled NFTokenAcceptOffer.cpp:56 - if (*bf <= beast::zero)
		if n.NFTokenBrokerFee.IsZero() {
			return tx.Errorf(tx.TemMALFORMED, "NFTokenBrokerFee must be greater than zero")
		}
	}

	// Validate offer IDs are valid hex strings (64 characters = 32 bytes)
	if n.NFTokenSellOffer != "" && len(n.NFTokenSellOffer) != 64 {
		return tx.Errorf(tx.TemMALFORMED, "invalid NFTokenSellOffer format")
	}
	if n.NFTokenBuyOffer != "" && len(n.NFTokenBuyOffer) != 64 {
		return tx.Errorf(tx.TemMALFORMED, "invalid NFTokenBuyOffer format")
	}

	return nil
}

func (n *NFTokenAcceptOffer) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(n)
}

// SetSellOffer sets the sell offer to accept
func (n *NFTokenAcceptOffer) SetSellOffer(offerID string) {
	n.NFTokenSellOffer = offerID
}

// SetBuyOffer sets the buy offer to accept
func (n *NFTokenAcceptOffer) SetBuyOffer(offerID string) {
	n.NFTokenBuyOffer = offerID
}

func (n *NFTokenAcceptOffer) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureNonFungibleTokensV1}
}

// Reference: rippled NFTokenAcceptOffer.cpp preclaim + doApply
// IMPORTANT: Check order must match rippled's preclaim exactly:
//  1. Load offers (existence, expiration, negative amount)
//  2. Brokered mode header checks (tokenID/issue match, sell>buy, destinations, broker fee)
//  3. Buy offer checks (type, own offer, ownership, fund, auth)
//  4. Sell offer checks (type, own offer, ownership, fund, auth)
//  5. Transfer fee issuer checks
func (n *NFTokenAcceptOffer) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("nftoken accept offer apply",
		"account", n.Account,
		"buyOffer", n.NFTokenBuyOffer,
		"sellOffer", n.NFTokenSellOffer,
		"brokerFee", n.NFTokenBrokerFee,
	)

	accountID := ctx.AccountID

	// --- Step 1: Load offers (checkOffer equivalent) ---
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 66-97
	var buyOffer, sellOffer *state.NFTokenOfferData
	var buyOfferKey, sellOfferKey keylet.Keylet
	// Track whether offer amounts are negative (from raw binary, since
	// NFTokenOfferData.Amount is uint64 and loses sign info).
	// These flags are used both for fixNFTokenNegOffer (temBAD_OFFER)
	// and for the pay() negative check (tecINTERNAL).
	var buyOfferNegative, sellOfferNegative bool

	if n.NFTokenBuyOffer != "" {
		buyOfferIDBytes, err := hex.DecodeString(n.NFTokenBuyOffer)
		if err != nil || len(buyOfferIDBytes) != 32 {
			return tx.TemINVALID
		}
		var buyOfferKeyBytes [32]byte
		copy(buyOfferKeyBytes[:], buyOfferIDBytes)
		buyOfferKey = keylet.Keylet{Type: entry.TypeNFTokenOffer, Key: buyOfferKeyBytes}

		// Zero offer ID check
		var zeroID [32]byte
		if buyOfferKeyBytes == zeroID {
			return tx.TecOBJECT_NOT_FOUND
		}

		buyOfferData, err := ctx.View.Read(buyOfferKey)
		if err != nil || buyOfferData == nil {
			ctx.Log.Warn("nftoken accept offer: buy offer not found",
				"buyOffer", n.NFTokenBuyOffer,
			)
			return tx.TecOBJECT_NOT_FOUND
		}
		buyOffer, err = state.ParseNFTokenOffer(buyOfferData)
		if err != nil {
			ctx.Log.Error("nftoken accept offer: failed to parse buy offer", "error", err)
			return tx.TefINTERNAL
		}

		// Check expiration
		if buyOffer.Expiration != 0 && buyOffer.Expiration <= ctx.Config.ParentCloseTime {
			ctx.Log.Warn("nftoken accept offer: buy offer expired")
			return tx.TecEXPIRED
		}

		// Detect negative amounts from raw binary (since uint64 loses sign)
		buyOfferNegative = isOfferAmountNegative(buyOfferData)

		// fixNFTokenNegOffer: reject negative amount offers
		// Reference: rippled NFTokenAcceptOffer.cpp checkOffer lines 80-87
		if ctx.Rules().Enabled(amendment.FeatureFixNFTokenNegOffer) {
			if buyOfferNegative {
				return tx.TemBAD_OFFER
			}
		}
	}

	if n.NFTokenSellOffer != "" {
		sellOfferIDBytes, err := hex.DecodeString(n.NFTokenSellOffer)
		if err != nil || len(sellOfferIDBytes) != 32 {
			return tx.TemINVALID
		}
		var sellOfferKeyBytes [32]byte
		copy(sellOfferKeyBytes[:], sellOfferIDBytes)
		sellOfferKey = keylet.Keylet{Type: entry.TypeNFTokenOffer, Key: sellOfferKeyBytes}

		// Zero offer ID check
		var zeroID [32]byte
		if sellOfferKeyBytes == zeroID {
			return tx.TecOBJECT_NOT_FOUND
		}

		sellOfferData, err := ctx.View.Read(sellOfferKey)
		if err != nil || sellOfferData == nil {
			ctx.Log.Warn("nftoken accept offer: sell offer not found",
				"sellOffer", n.NFTokenSellOffer,
			)
			return tx.TecOBJECT_NOT_FOUND
		}
		sellOffer, err = state.ParseNFTokenOffer(sellOfferData)
		if err != nil {
			ctx.Log.Error("nftoken accept offer: failed to parse sell offer", "error", err)
			return tx.TefINTERNAL
		}

		// Check expiration
		if sellOffer.Expiration != 0 && sellOffer.Expiration <= ctx.Config.ParentCloseTime {
			ctx.Log.Warn("nftoken accept offer: sell offer expired")
			return tx.TecEXPIRED
		}

		// Detect negative amounts from raw binary (since uint64 loses sign)
		sellOfferNegative = isOfferAmountNegative(sellOfferData)

		// fixNFTokenNegOffer: reject negative amount offers
		// Reference: rippled NFTokenAcceptOffer.cpp checkOffer lines 80-87
		if ctx.Rules().Enabled(amendment.FeatureFixNFTokenNegOffer) {
			if sellOfferNegative {
				return tx.TemBAD_OFFER
			}
		}
	}

	// --- Step 2: Brokered mode header checks ---
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 99-184
	if buyOffer != nil && sellOffer != nil {
		// Token IDs must match
		if buyOffer.NFTokenID != sellOffer.NFTokenID {
			return tx.TecNFTOKEN_BUY_SELL_MISMATCH
		}

		// Asset type must match
		buyIsXRP := buyOffer.AmountIOU == nil
		sellIsXRP := sellOffer.AmountIOU == nil
		if buyIsXRP != sellIsXRP {
			return tx.TecNFTOKEN_BUY_SELL_MISMATCH
		}
		if !buyIsXRP && !sellIsXRP {
			if buyOffer.AmountIOU.Currency != sellOffer.AmountIOU.Currency ||
				buyOffer.AmountIOU.Issuer != sellOffer.AmountIOU.Issuer {
				return tx.TecNFTOKEN_BUY_SELL_MISMATCH
			}
		}

		// Loop check (fixNonFungibleTokensV1_2)
		if ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
			if buyOffer.Owner == sellOffer.Owner {
				return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
			}
		}

		// Sell amount must not exceed buy amount.
		// Skip this check when offers have negative amounts (pre-fixNFTokenNegOffer):
		// negative amounts stored as uint64 lose their sign, making the
		// comparison meaningless. In rippled, the signed comparison on negative
		// amounts works naturally (e.g., -2M <= -1M is true).
		if !(buyOfferNegative || sellOfferNegative) {
			if buyIsXRP {
				if sellOffer.Amount > buyOffer.Amount {
					return tx.TecINSUFFICIENT_PAYMENT
				}
			} else {
				buyAmount := offerIOUToAmount(buyOffer)
				sellAmount := offerIOUToAmount(sellOffer)
				if sellAmount.Compare(buyAmount) > 0 {
					return tx.TecINSUFFICIENT_PAYMENT
				}
			}
		}

		// Destination checks (fixNonFungibleTokensV1_2: dest must be tx submitter)
		if buyOffer.HasDestination {
			if ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
				if buyOffer.Destination != accountID {
					return tx.TecNO_PERMISSION
				}
			} else if buyOffer.Destination != sellOffer.Owner && buyOffer.Destination != accountID {
				return tx.TecNFTOKEN_BUY_SELL_MISMATCH
			}
		}
		if sellOffer.HasDestination {
			if ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
				if sellOffer.Destination != accountID {
					return tx.TecNO_PERMISSION
				}
			} else if sellOffer.Destination != buyOffer.Owner && sellOffer.Destination != accountID {
				return tx.TecNFTOKEN_BUY_SELL_MISMATCH
			}
		}

		// Broker fee checks (skip when offers have negative amounts pre-amendment)
		if n.NFTokenBrokerFee != nil && !(buyOfferNegative || sellOfferNegative) {
			brokerFeeIsXRP := n.NFTokenBrokerFee.Currency == ""
			if brokerFeeIsXRP != buyIsXRP {
				return tx.TecNFTOKEN_BUY_SELL_MISMATCH
			}

			if buyIsXRP {
				brokerFee := uint64(n.NFTokenBrokerFee.Drops())
				if brokerFee >= buyOffer.Amount {
					return tx.TecINSUFFICIENT_PAYMENT
				}
				if sellOffer.Amount > buyOffer.Amount-brokerFee {
					return tx.TecINSUFFICIENT_PAYMENT
				}
			} else {
				brokerFeeIOU := *n.NFTokenBrokerFee
				buyAmount := offerIOUToAmount(buyOffer)
				sellAmount := offerIOUToAmount(sellOffer)
				if brokerFeeIOU.Compare(buyAmount) >= 0 {
					return tx.TecINSUFFICIENT_PAYMENT
				}
				remainder, _ := buyAmount.Sub(brokerFeeIOU)
				if sellAmount.Compare(remainder) > 0 {
					return tx.TecINSUFFICIENT_PAYMENT
				}
			}

			// Broker trustline authorization + deep freeze check (fixEnforceNFTokenTrustlineV2)
			if !n.NFTokenBrokerFee.IsNative() && ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
				brokerFeeIssuerID, err := state.DecodeAccountID(n.NFTokenBrokerFee.Issuer)
				if err == nil {
					if r := checkNFTTrustlineAuthorized(ctx.View, accountID, n.NFTokenBrokerFee.Currency, brokerFeeIssuerID); r != tx.TesSUCCESS {
						return r
					}
					// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 176-182
					if r := checkNFTTrustlineDeepFrozen(ctx.View, accountID, n.NFTokenBrokerFee.Currency, brokerFeeIssuerID, ctx.Rules()); r != tx.TesSUCCESS {
						return r
					}
				}
			}
		}
	}

	// --- Step 3: Buy offer individual checks ---
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 187-263
	if buyOffer != nil {
		// Type check
		if buyOffer.Flags&lsfSellNFToken != 0 {
			return tx.TecNFTOKEN_OFFER_TYPE_MISMATCH
		}

		// Cannot accept your own offer
		if buyOffer.Owner == accountID {
			return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
		}

		// Ownership check (non-brokered only)
		if sellOffer == nil {
			if _, _, _, found := findToken(ctx.View, accountID, buyOffer.NFTokenID); !found {
				return tx.TecNO_PERMISSION
			}
		}

		// Destination check (non-brokered only)
		if sellOffer == nil {
			if buyOffer.HasDestination && buyOffer.Destination != accountID {
				return tx.TecNO_PERMISSION
			}
		}

		// Fund check: buyer must have sufficient funds
		if buyOffer.AmountIOU != nil {
			buyAmount := offerIOUToAmount(buyOffer)
			if ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
				funds := tx.AccountFunds(ctx.View, buyOffer.Owner, buyAmount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
				if funds.Compare(buyAmount) < 0 {
					return tx.TecINSUFFICIENT_FUNDS
				}
			} else {
				funds := accountHoldsIOU(ctx.View, buyOffer.Owner, buyAmount)
				if funds.Compare(buyAmount) < 0 {
					return tx.TecINSUFFICIENT_FUNDS
				}
			}
		}

		// Trust line authorization checks (fixEnforceNFTokenTrustlineV2)
		if buyOffer.AmountIOU != nil && ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
			currency := buyOffer.AmountIOU.Currency
			issuerID := buyOffer.AmountIOU.Issuer
			if r := checkNFTTrustlineAuthorized(ctx.View, buyOffer.Owner, currency, issuerID); r != tx.TesSUCCESS {
				return r
			}
			// Direct buy offer: seller (acceptor) must be authorized + deep freeze check
			if sellOffer == nil {
				if r := checkNFTTrustlineAuthorized(ctx.View, accountID, currency, issuerID); r != tx.TesSUCCESS {
					return r
				}
				// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 255-261
				if r := checkNFTTrustlineDeepFrozen(ctx.View, accountID, currency, issuerID, ctx.Rules()); r != tx.TesSUCCESS {
					return r
				}
			}
		}
	}

	// --- Step 4: Sell offer individual checks ---
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 266-355
	if sellOffer != nil {
		// Type check
		if sellOffer.Flags&lsfSellNFToken == 0 {
			ctx.Log.Warn("nftoken accept offer: sell offer is actually a buy offer")
			return tx.TecNFTOKEN_OFFER_TYPE_MISMATCH
		}

		// Cannot accept your own offer
		if sellOffer.Owner == accountID {
			ctx.Log.Warn("nftoken accept offer: cannot accept own sell offer")
			return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
		}

		// Seller must own the token
		if _, _, _, found := findToken(ctx.View, sellOffer.Owner, sellOffer.NFTokenID); !found {
			return tx.TecNO_PERMISSION
		}

		// Destination check (non-brokered only)
		if buyOffer == nil {
			if sellOffer.HasDestination && sellOffer.Destination != accountID {
				return tx.TecNO_PERMISSION
			}
		}

		// Fund check for direct sell mode: buyer (acceptor) must have funds
		if sellOffer.AmountIOU != nil {
			fixV1_2 := ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2)
			if !fixV1_2 {
				sellAmount := offerIOUToAmount(sellOffer)
				funds := accountHoldsIOU(ctx.View, accountID, sellAmount)
				if funds.Compare(sellAmount) < 0 {
					return tx.TecINSUFFICIENT_FUNDS
				}
			} else if buyOffer == nil {
				sellAmount := offerIOUToAmount(sellOffer)
				funds := tx.AccountFunds(ctx.View, accountID, sellAmount, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
				if funds.Compare(sellAmount) < 0 {
					return tx.TecINSUFFICIENT_FUNDS
				}
			}
		}

		// Trust line authorization checks (fixEnforceNFTokenTrustlineV2)
		if sellOffer.AmountIOU != nil && ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
			currency := sellOffer.AmountIOU.Currency
			issuerID := sellOffer.AmountIOU.Issuer
			if r := checkNFTTrustlineAuthorized(ctx.View, sellOffer.Owner, currency, issuerID); r != tx.TesSUCCESS {
				return r
			}
			if buyOffer == nil {
				if r := checkNFTTrustlineAuthorized(ctx.View, accountID, currency, issuerID); r != tx.TesSUCCESS {
					return r
				}
			}
		}

		// Deep freeze check on sell offer owner (outside fixEnforceNFTokenTrustlineV2 gate)
		// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 350-353
		if sellOffer.AmountIOU != nil {
			currency := sellOffer.AmountIOU.Currency
			issuerID := sellOffer.AmountIOU.Issuer
			if r := checkNFTTrustlineDeepFrozen(ctx.View, sellOffer.Owner, currency, issuerID, ctx.Rules()); r != tx.TesSUCCESS {
				return r
			}
		}
	}

	// --- Step 5: Transfer fee issuer checks ---
	// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 358-392
	var tokenID [32]byte
	if buyOffer != nil {
		tokenID = buyOffer.NFTokenID
	} else if sellOffer != nil {
		tokenID = sellOffer.NFTokenID
	}

	transferFee := getNFTTransferFee(tokenID)
	if transferFee != 0 {
		nftMinterID := getNFTIssuer(tokenID)

		// Determine the offer amount
		var offerAmount *tx.Amount
		if buyOffer != nil && buyOffer.AmountIOU != nil {
			amt := offerIOUToAmount(buyOffer)
			offerAmount = &amt
		} else if sellOffer != nil && sellOffer.AmountIOU != nil {
			amt := offerIOUToAmount(sellOffer)
			offerAmount = &amt
		}

		if offerAmount != nil && !offerAmount.IsNative() {
			// fixEnforceNFTokenTrustline: issuer trust line check
			if ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustline) {
				nftFlags := getNFTFlagsFromID(tokenID)
				if nftFlags&nftFlagTrustLine == 0 {
					iouIssuerID, err := state.DecodeAccountID(offerAmount.Issuer)
					if err == nil && nftMinterID != iouIssuerID {
						trustLineKey := keylet.Line(nftMinterID, iouIssuerID, offerAmount.Currency)
						trustLineData, _ := ctx.View.Read(trustLineKey)
						if trustLineData == nil {
							return tx.TecNO_LINE
						}
					}
				}
			}

			// fixEnforceNFTokenTrustlineV2: issuer auth + deep freeze check
			if ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustlineV2) {
				iouIssuerID, err := state.DecodeAccountID(offerAmount.Issuer)
				if err == nil {
					if r := checkNFTTrustlineAuthorized(ctx.View, nftMinterID, offerAmount.Currency, iouIssuerID); r != tx.TesSUCCESS {
						return r
					}
					// Reference: rippled NFTokenAcceptOffer.cpp preclaim lines 387-390
					if r := checkNFTTrustlineDeepFrozen(ctx.View, nftMinterID, offerAmount.Currency, iouIssuerID, ctx.Rules()); r != tx.TesSUCCESS {
						return r
					}
				}
			}
		}
	}

	// --- Dispatch to mode-specific doApply ---
	// Brokered mode (both offers)
	if buyOffer != nil && sellOffer != nil {
		return n.executeBrokeredMode(ctx, accountID, buyOffer, sellOffer, buyOfferKey, sellOfferKey,
			buyOfferNegative, sellOfferNegative)
	}

	// Direct mode - sell offer only
	// Pre-amendment negative amount guard: rippled's pay() (line 404) checks
	// `if (amount < beast::zero) return tecINTERNAL;`. In direct mode,
	// pay() is always called when amount != 0, so negative amounts hit this.
	// Reference: rippled NFTokenAcceptOffer.cpp pay() line 404
	if sellOffer != nil {
		if sellOfferNegative && !ctx.Rules().Enabled(amendment.FeatureFixNFTokenNegOffer) {
			return tx.TecINTERNAL
		}
		return n.acceptNFTokenSellOfferDirect(ctx, accountID, sellOffer, sellOfferKey)
	}

	// Direct mode - buy offer only
	if buyOffer != nil {
		if buyOfferNegative && !ctx.Rules().Enabled(amendment.FeatureFixNFTokenNegOffer) {
			return tx.TecINTERNAL
		}
		return n.acceptNFTokenBuyOfferDirect(ctx, accountID, buyOffer, buyOfferKey)
	}

	return tx.TemINVALID
}

// iouPreclaimChecks is no longer used — its logic has been moved into Apply()
// to match rippled's exact check ordering. Kept as a comment for reference.

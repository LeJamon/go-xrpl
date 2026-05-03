package nftoken

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all NFToken-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeNFTokenMint, func() tx.Transaction {
		return &NFTokenMint{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenMint, "")}
	})
	tx.Register(tx.TypeNFTokenBurn, func() tx.Transaction {
		return &NFTokenBurn{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenBurn, "")}
	})
	tx.Register(tx.TypeNFTokenCreateOffer, func() tx.Transaction {
		return &NFTokenCreateOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCreateOffer, "")}
	})
	tx.Register(tx.TypeNFTokenCancelOffer, func() tx.Transaction {
		return &NFTokenCancelOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCancelOffer, "")}
	})
	tx.Register(tx.TypeNFTokenAcceptOffer, func() tx.Transaction {
		return &NFTokenAcceptOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenAcceptOffer, "")}
	})
	tx.Register(tx.TypeNFTokenModify, func() tx.Transaction {
		return &NFTokenModify{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenModify, "")}
	})
}

package nftoken

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all NFToken-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeNFTokenMint,
		New: func() tx.Transaction {
			return &NFTokenMint{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenMint, "")}
		},
		Delegation:   tx.Delegable,
		Privileges:   tx.PrivChangeNFTCounts,
		Consequences: tx.NormalConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeNFTokenBurn,
		New: func() tx.Transaction {
			return &NFTokenBurn{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenBurn, "")}
		},
		Delegation:   tx.Delegable,
		Privileges:   tx.PrivChangeNFTCounts,
		Consequences: tx.NormalConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeNFTokenCreateOffer,
		New: func() tx.Transaction {
			return &NFTokenCreateOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCreateOffer, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.NormalConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeNFTokenCancelOffer,
		New: func() tx.Transaction {
			return &NFTokenCancelOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenCancelOffer, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.NormalConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeNFTokenAcceptOffer,
		New: func() tx.Transaction {
			return &NFTokenAcceptOffer{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenAcceptOffer, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.NormalConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeNFTokenModify,
		New: func() tx.Transaction {
			return &NFTokenModify{BaseTx: *tx.NewBaseTx(tx.TypeNFTokenModify, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.NormalConsequences,
	})
}

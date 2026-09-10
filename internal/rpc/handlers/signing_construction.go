package handlers

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
)

func signingChecksEnabled(ctx *types.RpcContext) bool {
	return ctx.Services == nil || ctx.Services.Ledger() == nil || !ctx.Services.Ledger().IsStandalone()
}

func validateSigningConstruction(ctx *types.RpcContext, blob string, rules *amendment.Rules) *rpcerrors.RpcError {
	raw, err := hex.DecodeString(blob)
	if err != nil {
		return rpcInternalError("signing: transaction decoding failed", err)
	}
	transaction, err := tx.ParseFromBinary(raw)
	if err != nil {
		return rpcInternalError("signing: transaction construction failed", err)
	}
	if sign.CheckSTTxSignature(transaction, rules, signingChecksEnabled(ctx)) != "" ||
		tx.TransactionLocalChecksFailureReason(transaction) != "" {
		return rpcerrors.RpcErrorSigningInvalidSignature()
	}
	return nil
}

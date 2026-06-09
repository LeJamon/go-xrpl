package payment

import (
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
)

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects persist.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (p *Payment) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	credential.RemoveExpiredCredentials(ctx, p.CredentialIDs)
	return tx.TecEXPIRED
}

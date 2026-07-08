package lending

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Loan-family Apply methods are implemented in loan_set_apply.go etc.; these
// temporary bodies keep the package building while they are filled in.

func (l *LoanSet) Apply(ctx *tx.ApplyContext) ter.Result    { return ter.TefINTERNAL }
func (l *LoanDelete) Apply(ctx *tx.ApplyContext) ter.Result { return ter.TefINTERNAL }
func (l *LoanManage) Apply(ctx *tx.ApplyContext) ter.Result { return ter.TefINTERNAL }
func (l *LoanPay) Apply(ctx *tx.ApplyContext) ter.Result    { return ter.TefINTERNAL }

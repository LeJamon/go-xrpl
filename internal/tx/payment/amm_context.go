package payment

import "github.com/LeJamon/go-xrpl/internal/ledger/state"

// AMMContext maintains AMM info per overall payment engine execution and
// individual iteration. Only one instance is created in RippleCalculate().
// The reference is percolated through calls to AMMLiquidity class,
// which handles AMM offer generation.
//
// Reference: rippled/src/xrpld/app/paths/AMMContext.h

const (
	// AMMMaxIterations restricts the number of AMM offers.
	// If this restriction is removed then need to restrict in some other way
	// because AMM offers are not counted in the BookStep offer counter.
	AMMMaxIterations = 30
)

// AMMContext tracks AMM state across payment engine iterations.
type AMMContext struct {
	numberContext state.NumberContext

	// account is the tx sender, needed to get AMM trading fee in BookStep
	account [20]byte

	// multiPath is true if the payment has multiple active strands
	multiPath bool

	// ammUsed is true if an AMM offer was consumed in the current iteration
	ammUsed bool

	// ammIters counts payment engine iterations that consumed AMM offers
	ammIters uint16
}

// NewAMMContext creates a new AMMContext for a payment.
func NewAMMContext(account [20]byte, multiPath bool, numberContext state.NumberContext) *AMMContext {
	return &AMMContext{numberContext: numberContext, account: account, multiPath: multiPath}
}

func (c *AMMContext) numberMath() numberMath { return numberMath{ctx: c.numberContext} }

// MultiPath returns true if the payment has multiple paths.
func (c *AMMContext) MultiPath() bool {
	return c.multiPath
}

// SetMultiPath sets whether the payment has multiple paths.
func (c *AMMContext) SetMultiPath(mp bool) {
	c.multiPath = mp
}

// SetAMMUsed marks that an AMM offer was consumed in this iteration.
func (c *AMMContext) SetAMMUsed() {
	c.ammUsed = true
}

// Update increments ammIters if AMM was used, then resets ammUsed.
// Called after each payment engine iteration completes.
func (c *AMMContext) Update() {
	if c.ammUsed {
		c.ammIters++
	}
	c.ammUsed = false
}

// MaxItersReached returns true if the AMM iteration limit has been reached.
func (c *AMMContext) MaxItersReached() bool {
	return c.ammIters >= AMMMaxIterations
}

// CurIters returns the current count of iterations that consumed AMM offers.
func (c *AMMContext) CurIters() uint16 {
	return c.ammIters
}

// Account returns the transaction sender account.
func (c *AMMContext) Account() [20]byte {
	return c.account
}

// Clear resets the ammUsed flag. Called at the start of each strand execution
// attempt since the strand may fail.
func (c *AMMContext) Clear() {
	c.ammUsed = false
}

// Package resource implements per-endpoint load tracking and
// charge-based disconnect, mirroring rippled's Resource::Manager
// (rippled/include/xrpl/resource and src/libxrpl/resource).
//
// A Manager owns a table of Consumers keyed by endpoint. Callers
// (typically peers) hold a Consumer and apply Charges to it for known
// expensive or invalid operations. The Manager exponentially decays
// each Consumer's balance over a fixed window; when the balance crosses
// the drop threshold the next Charge returns Drop, signalling the
// caller to tear the endpoint down.
//
// Endpoint keys persist after a Consumer is released so a peer that
// reconnects from the same address inherits its prior balance — this
// is what makes the system robust to flap-and-retry abuse.
package resource

import "fmt"

// Charge is a load cost with a human-readable label. Charges are value
// types: callers compose them in package-level vars (see Fees) and pass
// them by value into Consumer.Charge. Mirrors rippled's
// ripple::Resource::Charge.
type Charge struct {
	cost  int
	label string
}

func NewCharge(cost int, label string) Charge {
	return Charge{cost: cost, label: label}
}

func (c Charge) Cost() int { return c.cost }

func (c Charge) Label() string { return c.label }

// String matches rippled's `label ($cost)` format.
func (c Charge) String() string {
	return fmt.Sprintf("%s ($%d)", c.label, c.cost)
}

// Scale mirrors rippled's Charge::operator*.
func (c Charge) Scale(m int) Charge {
	return Charge{cost: c.cost * m, label: c.label}
}

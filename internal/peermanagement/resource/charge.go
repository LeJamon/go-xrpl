// Package resource implements bounded per-endpoint peer load tracking.
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

// Charge is a load cost with a human-readable label.
type Charge struct {
	cost  int
	label string
}

func NewCharge(cost int, label string) Charge {
	if cost < 0 {
		cost = 0
	}
	return Charge{cost: cost, label: label}
}

func (c Charge) Cost() int { return c.cost }

func (c Charge) String() string {
	return fmt.Sprintf("%s ($%d)", c.label, c.cost)
}

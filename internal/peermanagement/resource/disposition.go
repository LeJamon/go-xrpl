package resource

// Disposition is the result of charging a Consumer.
type Disposition int

const (
	// Ok means the Charge fits within the consumer's budget.
	Ok Disposition = iota

	// Warn means the consumer has crossed the warning threshold and
	// should be notified that consumption is high. Subsequent charges
	// continue to return Warn until the balance decays below threshold
	// or crosses into Drop.
	Warn

	// Drop means the consumer has crossed the drop threshold and the
	// caller must tear down the endpoint.
	Drop
)

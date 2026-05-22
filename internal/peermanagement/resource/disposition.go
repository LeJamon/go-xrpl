package resource

// Disposition is the result of charging a Consumer. Mirrors rippled's
// ripple::Resource::Disposition.
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

// String returns the lowercase disposition name (matches rippled's
// enum stringification used in logs).
func (d Disposition) String() string {
	switch d {
	case Ok:
		return "ok"
	case Warn:
		return "warn"
	case Drop:
		return "drop"
	default:
		return "unknown"
	}
}

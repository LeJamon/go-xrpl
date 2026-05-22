package resource

// Kind classifies a Consumer. Mirrors rippled's
// ripple::Resource::Kind.
type Kind int

const (
	// KindInbound is a peer/client connection accepted by this node.
	KindInbound Kind = iota

	// KindOutbound is a peer connection initiated by this node.
	KindOutbound

	// KindUnlimited is a privileged endpoint (e.g. cluster member,
	// admin) for which Charge is a no-op — balance stays at zero and
	// disposition is always Ok.
	KindUnlimited
)

func (k Kind) String() string {
	switch k {
	case KindInbound:
		return "inbound"
	case KindOutbound:
		return "outbound"
	case KindUnlimited:
		return "unlimited"
	default:
		return "unknown"
	}
}

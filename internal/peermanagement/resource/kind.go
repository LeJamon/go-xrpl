package resource

type Kind int

const (
	// KindInbound is a peer/client connection accepted by this node.
	KindInbound Kind = iota

	// KindOutbound is a peer connection initiated by this node.
	KindOutbound

	// KindUnlimited is a privileged administrative endpoint for which
	// Charge is a no-op.
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

package store

type ObjectType uint32

const (
	TypeUnknown     ObjectType = 0
	TypeLedger      ObjectType = 1
	TypeAccount     ObjectType = 3
	TypeTransaction ObjectType = 4
	TypeDummy       ObjectType = 512
)

type Node struct {
	typ  ObjectType
	hash [32]byte
	data []byte
}

func New(typ ObjectType, data []byte, hash [32]byte) *Node {
	return &Node{
		typ:  typ,
		hash: hash,
		data: data,
	}
}

func (n *Node) Type() ObjectType {
	return n.typ
}

func (n *Node) Hash() [32]byte {
	return n.hash
}

func (n *Node) Data() []byte {
	return n.data
}

func (t ObjectType) String() string {
	switch t {
	case TypeUnknown:
		return "Unknown"
	case TypeLedger:
		return "Ledger"
	case TypeAccount:
		return "Account"
	case TypeTransaction:
		return "Transaction"
	case TypeDummy:
		return "Dummy"
	default:
		return "Invalid"
	}
}

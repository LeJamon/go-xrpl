package selector

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/LeJamon/go-xrpl/protocol"
)

var (
	ErrInvalidSelector = errors.New("invalid ledger selector")
	ErrInvalidIndex    = fmt.Errorf("%w: invalid ledger index", ErrInvalidSelector)
	ErrInvalidHash     = fmt.Errorf("%w: invalid ledger hash", ErrInvalidSelector)
	ErrLedgerNotFound  = errors.New("ledger not found")
)

type Kind uint8

const (
	KindAbsent Kind = iota
	KindCurrent
	KindClosed
	KindValidated
	KindSequence
	KindHash
)

type Selector struct {
	kind     Kind
	sequence uint32
	hash     [32]byte
}

func Absent() Selector {
	return Selector{kind: KindAbsent}
}

func Current() Selector {
	return Selector{kind: KindCurrent}
}

func Closed() Selector {
	return Selector{kind: KindClosed}
}

func Validated() Selector {
	return Selector{kind: KindValidated}
}

func FromSequence(sequence uint32) Selector {
	return Selector{kind: KindSequence, sequence: sequence}
}

func FromHash(hash [32]byte) Selector {
	return Selector{kind: KindHash, hash: hash}
}

func Parse(value string) (Selector, error) {
	if len(value) == 64 {
		return ParseHash(value)
	}
	return ParseIndex(value)
}

func ParseIndex(value string) (Selector, error) {
	switch value {
	case "":
		return Absent(), nil
	case "current":
		return Current(), nil
	case "closed":
		return Closed(), nil
	case "validated":
		return Validated(), nil
	}

	digits := value
	if digits[0] == '+' {
		digits = digits[1:]
		if digits == "" {
			return Selector{}, ErrInvalidIndex
		}
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return Selector{}, ErrInvalidIndex
		}
	}
	sequence, err := strconv.ParseUint(digits, 10, 32)
	if err != nil {
		return Selector{}, ErrInvalidIndex
	}
	return FromSequence(uint32(sequence)), nil
}

func ParseHash(value string) (Selector, error) {
	if value == "0" {
		return FromHash([32]byte{}), nil
	}
	hash, err := protocol.Hash256FromHex(value)
	if err != nil {
		return Selector{}, ErrInvalidHash
	}
	return FromHash(hash), nil
}

func (s Selector) Kind() Kind {
	return s.kind
}

func (s Selector) Sequence() (uint32, bool) {
	return s.sequence, s.kind == KindSequence
}

func (s Selector) Hash() ([32]byte, bool) {
	return s.hash, s.kind == KindHash
}

func (s Selector) String() string {
	switch s.kind {
	case KindAbsent:
		return ""
	case KindCurrent:
		return "current"
	case KindClosed:
		return "closed"
	case KindValidated:
		return "validated"
	case KindSequence:
		return strconv.FormatUint(uint64(s.sequence), 10)
	case KindHash:
		return protocol.Hash256Hex(s.hash)
	default:
		return ""
	}
}

type Ledger interface {
	Sequence() uint32
	Hash() [32]byte
	IsValidated() bool
}

type Lookup[T Ledger] func() (value T, found bool, err error)

type SequenceLookup[T Ledger] func(sequence uint32) (value T, found bool, err error)

type HashLookup[T Ledger] func(hash [32]byte) (value T, found bool, err error)

type Callbacks[T Ledger] struct {
	Absent     Lookup[T]
	Current    Lookup[T]
	Closed     Lookup[T]
	Validated  Lookup[T]
	BySequence SequenceLookup[T]
	ByHash     HashLookup[T]
}

type Result[T Ledger] struct {
	Value     T
	Selector  Selector
	Sequence  uint32
	Hash      [32]byte
	Validated bool
}

func Resolve[T Ledger](selection Selector, callbacks Callbacks[T]) (Result[T], error) {
	var (
		value T
		found bool
		err   error
	)

	switch selection.kind {
	case KindAbsent:
		if callbacks.Absent == nil {
			return Result[T]{}, missingCallback(KindAbsent)
		}
		value, found, err = callbacks.Absent()
	case KindCurrent:
		if callbacks.Current == nil {
			return Result[T]{}, missingCallback(KindCurrent)
		}
		value, found, err = callbacks.Current()
	case KindClosed:
		if callbacks.Closed == nil {
			return Result[T]{}, missingCallback(KindClosed)
		}
		value, found, err = callbacks.Closed()
	case KindValidated:
		if callbacks.Validated == nil {
			return Result[T]{}, missingCallback(KindValidated)
		}
		value, found, err = callbacks.Validated()
	case KindSequence:
		if callbacks.BySequence == nil {
			return Result[T]{}, missingCallback(KindSequence)
		}
		value, found, err = callbacks.BySequence(selection.sequence)
	case KindHash:
		if callbacks.ByHash == nil {
			return Result[T]{}, missingCallback(KindHash)
		}
		value, found, err = callbacks.ByHash(selection.hash)
	default:
		return Result[T]{}, ErrInvalidSelector
	}
	if err != nil {
		return Result[T]{}, err
	}
	if !found {
		return Result[T]{}, ErrLedgerNotFound
	}

	return Result[T]{
		Value:     value,
		Selector:  selection,
		Sequence:  value.Sequence(),
		Hash:      value.Hash(),
		Validated: value.IsValidated(),
	}, nil
}

func missingCallback(kind Kind) error {
	return fmt.Errorf("ledger selector resolver has no callback for kind %d", kind)
}

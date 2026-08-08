package enginefuzz

import (
	"encoding/binary"
	"fmt"
)

const (
	maxSteps = 40
	stepSize = 26
)

type amendmentProfile uint8

const profileV320 amendmentProfile = 0

func (p amendmentProfile) String() string {
	if p == profileV320 {
		return "rippled-v3.2.0-supported"
	}
	return fmt.Sprintf("unknown-%d", p)
}

type txKind uint8

const (
	kindPaymentXRP txKind = iota
	kindPaymentIOU
	kindAccountSet
	kindTrustSet
	kindOfferCreate
	kindOfferCancel
	numKinds
)

func (k txKind) String() string {
	switch k {
	case kindPaymentXRP:
		return "payment-xrp"
	case kindPaymentIOU:
		return "payment-iou"
	case kindAccountSet:
		return "accountset"
	case kindTrustSet:
		return "trustset"
	case kindOfferCreate:
		return "offercreate"
	case kindOfferCancel:
		return "offercancel"
	default:
		return fmt.Sprintf("unknown-%d", k)
	}
}

type traceStep struct {
	Kind       txKind
	From       uint8
	To         uint8
	Currency   uint8
	Option     uint8
	Amount     uint64
	Limit      uint64
	Offer      uint32
	CloseAfter bool
	InputStart int
	InputEnd   int
}

func (s traceStep) String() string {
	return fmt.Sprintf("kind=%s from=%d to=%d currency=%d option=%d amount=%d limit=%d offer=%d close=%t input=[%d,%d)",
		s.Kind, s.From, s.To, s.Currency, s.Option, s.Amount, s.Limit, s.Offer, s.CloseAfter, s.InputStart, s.InputEnd)
}

type trace struct {
	Profile amendmentProfile
	Steps   []traceStep
}

func decodeTrace(data []byte) trace {
	s := &stream{data: data}
	tr := trace{Profile: profileV320}
	if s.drained() {
		return tr
	}
	tr.Profile = amendmentProfile(s.index(1))
	for len(tr.Steps) < maxSteps && !s.drained() {
		start := s.offset()
		step := traceStep{
			Kind:       txKind(s.index(int(numKinds))),
			From:       uint8(s.index(4)),
			To:         uint8(s.index(4)),
			Currency:   uint8(s.index(2)),
			Option:     s.u8(),
			Amount:     1 + s.bounded(10_000_000_000),
			Limit:      1 + s.bounded(1_000_000),
			Offer:      s.u32(),
			CloseAfter: s.chance(32),
		}
		step.InputStart = start
		step.InputEnd = s.offset()
		tr.Steps = append(tr.Steps, step)
	}
	return tr
}

func encodeTrace(tr trace) []byte {
	data := make([]byte, 1, 1+len(tr.Steps)*stepSize)
	data[0] = byte(tr.Profile)
	for _, step := range tr.Steps {
		buf := make([]byte, stepSize)
		buf[0] = byte(step.Kind)
		buf[1] = step.From
		buf[2] = step.To
		buf[3] = step.Currency
		buf[4] = step.Option
		binary.BigEndian.PutUint64(buf[5:13], step.Amount-1)
		binary.BigEndian.PutUint64(buf[13:21], step.Limit-1)
		binary.BigEndian.PutUint32(buf[21:25], step.Offer)
		buf[25] = 255
		if step.CloseAfter {
			buf[25] = 0
		}
		data = append(data, buf...)
	}
	return data
}

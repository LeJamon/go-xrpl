package enginefuzz

import (
	"math"
	"testing"
)

func TestStreamBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		call func()
	}{
		{name: "index-zero", call: func() { (&stream{}).index(0) }},
		{name: "index-negative", call: func() { (&stream{}).index(-1) }},
		{name: "bounded-zero", call: func() { (&stream{}).bounded(0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid bound did not panic")
				}
			}()
			test.call()
		})
	}
}

func TestTraceRoundTripPreservesWideDomains(t *testing.T) {
	want := trace{
		Profile: profileV320,
		Steps: []traceStep{{
			Kind:       kindTrustSet,
			From:       3,
			To:         2,
			Currency:   1,
			Option:     math.MaxUint8,
			Amount:     10_000_000_000,
			Limit:      1_000_000,
			Offer:      math.MaxUint32,
			CloseAfter: true,
		}},
	}

	got := decodeTrace(encodeTrace(want))
	if len(got.Steps) != 1 {
		t.Fatalf("decoded %d steps, want 1", len(got.Steps))
	}
	step := got.Steps[0]
	if step.Amount != want.Steps[0].Amount || step.Limit != want.Steps[0].Limit || step.Offer != want.Steps[0].Offer {
		t.Fatalf("wide fields changed: got amount=%d limit=%d offer=%d", step.Amount, step.Limit, step.Offer)
	}
	if step.InputStart != 1 || step.InputEnd != 1+stepSize {
		t.Fatalf("input span = [%d,%d), want [1,%d)", step.InputStart, step.InputEnd, 1+stepSize)
	}
}

func TestDecodeTraceCapsWork(t *testing.T) {
	data := make([]byte, 1+(maxSteps+5)*stepSize)
	if steps := len(decodeTrace(data).Steps); steps != maxSteps {
		t.Fatalf("decoded %d steps, want cap %d", steps, maxSteps)
	}
}

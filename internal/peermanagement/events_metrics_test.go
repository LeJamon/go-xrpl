package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInboundMessagePreservesWireSize(t *testing.T) {
	event := &Event{Payload: []byte{1, 2, 3}, WireSize: 2}
	message := event.inboundMessage()

	assert.Equal(t, uint64(2), message.WireSize)
	assert.Equal(t, []byte{1, 2, 3}, message.Payload)
}

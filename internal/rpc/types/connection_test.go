package types

import (
	"sync"
	"testing"
)

func TestConnectionCancelIsConcurrentAndAuthoritative(t *testing.T) {
	connection := NewConnection("test", make(chan []byte, 1))
	if connection.Outbound() != connection.SendChannel {
		t.Fatal("Outbound did not return the canonical queue")
	}
	if !connection.TrySend([]byte("before")) {
		t.Fatal("initial send was rejected")
	}
	<-connection.Outbound()

	var cancelers sync.WaitGroup
	for range 32 {
		cancelers.Add(1)
		go func() {
			defer cancelers.Done()
			connection.Cancel()
		}()
	}
	cancelers.Wait()
	select {
	case <-connection.Done():
	default:
		t.Fatal("connection Done channel remained open after cancellation")
	}
	if connection.TrySend([]byte("after")) {
		t.Fatal("send succeeded after connection cancellation")
	}
}

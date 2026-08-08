package subscription

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConnectionCancelIsConcurrentAndAuthoritative(t *testing.T) {
	queue := make(chan []byte, 1)
	connection := NewConnection("test", queue)
	if connection.Outbound() != queue {
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

func TestConnectionSlowConsumerDisconnectsExactlyOnce(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		conn := NewConnection("slow", make(chan []byte))
		var disconnects atomic.Int32
		conn.SetDisconnect(func() { disconnects.Add(1) })
		for range MaxConsecutiveDrops * 3 {
			if conn.TrySend([]byte("event")) {
				t.Fatal("unbuffered queue unexpectedly accepted an event")
			}
		}
		if got := disconnects.Load(); got != 1 {
			t.Fatalf("disconnect callbacks = %d, want 1", got)
		}
		stats := conn.Stats()
		if !stats.Terminal || stats.Disconnects != 1 || stats.Drops != MaxConsecutiveDrops {
			t.Fatalf("stats = %+v", stats)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		conn := NewConnection("slow-concurrent", make(chan []byte))
		var disconnects atomic.Int32
		conn.SetDisconnect(func() { disconnects.Add(1) })
		var wg sync.WaitGroup
		for range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn.TrySend([]byte("event"))
			}()
		}
		wg.Wait()
		if got := disconnects.Load(); got != 1 {
			t.Fatalf("disconnect callbacks = %d, want 1", got)
		}
		if stats := conn.Stats(); !stats.Terminal || stats.Disconnects != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	})
}

func TestConnectionCustomDropLimit(t *testing.T) {
	connection := NewConnection("websocket", make(chan []byte, 1))
	connection.SetDropLimit(1)
	if !connection.TrySend([]byte("queued")) {
		t.Fatal("initial send was rejected")
	}
	var disconnects atomic.Int32
	connection.SetDisconnect(func() { disconnects.Add(1) })
	if connection.TrySend([]byte("overflow")) {
		t.Fatal("overflow send unexpectedly succeeded")
	}
	stats := connection.Stats()
	if !stats.Terminal || stats.Drops != 1 || stats.Disconnects != 1 || disconnects.Load() != 1 {
		t.Fatalf("stats = %+v, callbacks = %d", stats, disconnects.Load())
	}
}

func TestConnectionCancelFencesBlockedEncode(t *testing.T) {
	connection := NewConnection("blocked", make(chan []byte, 2))
	encodeEntered := make(chan struct{})
	releaseEncode := make(chan struct{})
	connection.SetEncodeOutbound(func(data []byte) []byte {
		close(encodeEntered)
		<-releaseEncode
		return data
	})
	sendDone := make(chan bool, 1)
	go func() { sendDone <- connection.TrySend([]byte("before")) }()
	<-encodeEntered
	cancelDone := make(chan struct{})
	go func() {
		connection.Cancel()
		close(cancelDone)
	}()
	select {
	case <-cancelDone:
		t.Fatal("Cancel returned before the active enqueue left the send gate")
	default:
	}
	close(releaseEncode)
	if !<-sendDone {
		t.Fatal("enqueue active before Cancel was unexpectedly rejected")
	}
	<-cancelDone
	if connection.TrySend([]byte("after")) {
		t.Fatal("enqueue succeeded after Cancel returned")
	}
	if got := <-connection.Outbound(); string(got) != "before" {
		t.Fatalf("queued message = %q", got)
	}
	select {
	case got := <-connection.Outbound():
		t.Fatalf("post-cancel enqueue = %q", got)
	default:
	}
}

func TestConnectionParentCancellationRejectsBlockedEncode(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	connection := NewConnectionWithContext(parent, "parent", make(chan []byte, 1))
	encodeEntered := make(chan struct{})
	releaseEncode := make(chan struct{})
	connection.SetEncodeOutbound(func(data []byte) []byte {
		close(encodeEntered)
		<-releaseEncode
		return data
	})
	sendDone := make(chan bool, 1)
	go func() { sendDone <- connection.TrySend([]byte("event")) }()
	<-encodeEntered
	cancel()
	close(releaseEncode)
	if <-sendDone {
		t.Fatal("enqueue succeeded after the parent context was canceled")
	}
	select {
	case got := <-connection.Outbound():
		t.Fatalf("canceled enqueue = %q", got)
	default:
	}
}

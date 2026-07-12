package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func TestPathFindCreateInvalidReplacementClearsOldSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn := &WebSocketConnection{
		ctx:         ctx,
		sendChannel: make(chan []byte, 1),
	}
	old := &PathFindSession{}
	conn.installPathFindSession(old)
	before := conn.pathFindGeneration

	ws := NewWebSocketServer(0, nil)
	ws.handlePathFindCreate(conn, &types.RPCContext{Context: ctx}, types.WebSocketCommand{
		ID:     1,
		Params: json.RawMessage(`{"subcommand":"create"}`),
	})

	_, active := conn.snapshotPathFindUpdate()
	require.False(t, active)
	require.Equal(t, before+1, conn.pathFindGeneration)
	require.NotEmpty(t, conn.sendChannel)
}

func TestPathFindInFlightUpdateDiscardedAfterSessionChange(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WebSocketConnection, *PathFindSession)
	}{
		{
			name: "close",
			mutate: func(conn *WebSocketConnection, _ *PathFindSession) {
				require.NotNil(t, conn.clearPathFindSession())
			},
		},
		{
			name: "replacement",
			mutate: func(conn *WebSocketConnection, replacement *PathFindSession) {
				require.NotNil(t, conn.clearPathFindSession())
				conn.installPathFindSession(replacement)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			outbound := make(chan []byte, 1)
			conn := &WebSocketConnection{
				legacy: &types.Connection{SendChannel: outbound},
			}
			old := &PathFindSession{}
			replacement := &PathFindSession{}
			conn.installPathFindSession(old)

			targetReady := make(chan pathFindUpdateTarget)
			resume := make(chan struct{})
			sent := make(chan bool, 1)
			go func() {
				target, ok := conn.snapshotPathFindUpdate()
				if !ok {
					targetReady <- pathFindUpdateTarget{}
					return
				}
				targetReady <- target
				<-resume
				sent <- target.trySend([]byte(`{"type":"path_find"}`))
			}()

			target := <-targetReady
			require.Same(t, old, target.session)
			test.mutate(conn, replacement)
			close(resume)
			require.False(t, <-sent)
			require.Empty(t, outbound)

			if test.name == "replacement" {
				current, ok := conn.snapshotPathFindUpdate()
				require.True(t, ok)
				require.Same(t, replacement, current.session)
			}
		})
	}
}

func TestPathFindCurrentUpdateEnqueues(t *testing.T) {
	outbound := make(chan []byte, 1)
	conn := &WebSocketConnection{legacy: &types.Connection{SendChannel: outbound}}
	conn.installPathFindSession(&PathFindSession{})
	target, ok := conn.snapshotPathFindUpdate()
	require.True(t, ok)

	message := []byte(`{"type":"path_find"}`)
	require.True(t, target.trySend(message))
	require.Equal(t, message, <-outbound)
}

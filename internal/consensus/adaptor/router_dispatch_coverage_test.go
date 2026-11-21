package adaptor

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
)

// routerIgnoredMessages lists message types the overlay may deliver to the
// router's dispatch lanes that the router deliberately drops, with the reason.
var routerIgnoredMessages = map[message.MessageType]string{}

// TestRouterHandlesEveryOverlayRoutedMessage sends every wire message type
// over a real overlay connection and requires each type delivered on a lane
// the router consumes to be handled there. Types delivered on the dispatch
// lanes must have a case in handleMessage or a routerIgnoredMessages entry;
// the transaction and manifest lanes feed type-specific workers.
func TestRouterHandlesEveryOverlayRoutedMessage(t *testing.T) {
	handled := routerDispatchCases(t)
	for msgType, reason := range routerIgnoredMessages {
		require.NotEmpty(t, reason, msgType.String())
		require.False(t, handled[msgType], "%s is both handled and ignored", msgType)
	}

	delivered := overlayDeliveredTypes(t)
	require.NotEmpty(t, delivered["ledger-data"], "probe did not reach the ledger-data lane")
	for lane, types := range delivered {
		for msgType := range types {
			switch lane {
			case "transaction":
				assert.Equal(t, message.TypeTransaction, msgType, "transaction lane")
			case "manifests":
				assert.Equal(t, message.TypeManifests, msgType, "manifest lane")
			default:
				_, ignored := routerIgnoredMessages[msgType]
				assert.True(t, handled[msgType] || ignored,
					"%s is delivered on the %s lane but the router neither handles nor ignores it", msgType, lane)
			}
		}
	}
}

// routerDispatchCases reads the message types (*Router).handleMessage
// switches on from its source, so the assertion tracks the real switch.
func routerDispatchCases(t *testing.T) map[message.MessageType]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "router_dispatch.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	cases := make(map[message.MessageType]bool)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "handleMessage" {
			continue
		}
		for _, stmt := range fn.Body.List {
			sw, ok := stmt.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			if tag, ok := sw.Tag.(*ast.Ident); !ok || tag.Name != "msgType" {
				continue
			}
			for _, clause := range sw.Body.List {
				for _, expr := range clause.(*ast.CaseClause).List {
					sel, ok := expr.(*ast.SelectorExpr)
					require.True(t, ok, "unexpected case expression in handleMessage")
					cases[wireMessageType(t, sel.Sel.Name)] = true
				}
			}
		}
	}
	require.NotEmpty(t, cases, "handleMessage dispatch switch not found")
	return cases
}

// wireMessageType resolves a message.TypeXxx constant name to its wire type
// through the protobuf enum name mtXXX.
func wireMessageType(t *testing.T, constant string) message.MessageType {
	t.Helper()
	var enum strings.Builder
	enum.WriteString("mt")
	for i, r := range strings.TrimPrefix(constant, "Type") {
		if i > 0 && unicode.IsUpper(r) {
			enum.WriteByte('_')
		}
		enum.WriteRune(unicode.ToUpper(r))
	}
	code, ok := proto.MessageType_value[enum.String()]
	require.True(t, ok, "message.%s does not resolve to a wire message type", constant)
	return message.MessageType(code)
}

// overlayDeliveredTypes connects two overlays with every optional feature
// negotiated, sends one frame of each wire type and records which types the
// receiver delivers on each lane the router consumes. A trailing marker frame
// on every inbound peer lane proves all earlier frames were dispatched.
func overlayDeliveredTypes(t *testing.T) map[string]map[message.MessageType]bool {
	t.Helper()
	opts := make([]peermanagement.Option, 0, 5)
	opts = append(opts,
		peermanagement.WithCompression(false),
		peermanagement.WithLedgerReplay(true),
		peermanagement.WithReduceRelay(true),
	)
	connected := make(chan peermanagement.PeerID, 1)
	source := startRunningManifestOverlay(t, opts...)
	source.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
		select {
		case connected <- peerID:
		default:
		}
	})
	sink := startRunningManifestOverlay(t, append(opts,
		peermanagement.WithMaxOutbound(1),
		peermanagement.WithFixedPeers(source.overlay.ListenAddr()),
	)...)

	var peerID peermanagement.PeerID
	select {
	case peerID = <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("overlays did not connect")
	}

	lanes := map[string]<-chan *peermanagement.InboundMessage{
		"general":           sink.overlay.Messages(),
		"ledger-data":       sink.overlay.LedgerDataMessages(),
		"consensus":         sink.overlay.ConsensusMessages(),
		"consensus-control": sink.overlay.ConsensusControlMessages(),
		"transaction":       sink.overlay.TxMessages(),
		"manifests":         sink.overlay.ManifestMessages(),
	}
	marker := protowire.AppendTag(nil, 99, protowire.BytesType)
	marker = protowire.AppendBytes(marker, []byte("marker"))
	markers := map[message.MessageType]string{
		message.TypeGetLedger:  "general",
		message.TypeLedgerData: "ledger-data",
		message.TypeValidation: "consensus",
		message.TypeHaveSet:    "consensus-control",
		message.TypeManifests:  "manifests",
	}

	delivered := make(map[string]map[message.MessageType]bool, len(lanes))
	marked := make(chan struct{}, len(markers))
	stop := make(chan struct{})
	var drained sync.WaitGroup
	defer func() {
		close(stop)
		drained.Wait()
	}()
	for name, lane := range lanes {
		seen := make(map[message.MessageType]bool)
		delivered[name] = seen
		drained.Go(func() {
			for {
				select {
				case <-stop:
					return
				case msg, ok := <-lane:
					if !ok {
						return
					}
					if bytes.Equal(msg.Payload, marker) && markers[msg.Type] == name {
						marked <- struct{}{}
					} else {
						seen[msg.Type] = true
					}
					_ = msg.Close()
				}
			}
		})
	}

	send := func(msgType message.MessageType, payload []byte) {
		frame, err := message.BuildWireMessage(msgType, payload)
		require.NoError(t, err)
		require.NoError(t, source.overlay.Send(peerID, frame), msgType.String())
	}
	for code := range proto.MessageType_name {
		send(message.MessageType(code), nil)
	}
	fetchPackReply, err := message.Encode(&message.GetObjectByHash{ObjType: message.ObjectTypeFetchPack})
	require.NoError(t, err)
	send(message.TypeGetObjects, fetchPackReply)
	for msgType := range markers {
		send(msgType, marker)
	}

	for range markers {
		select {
		case <-marked:
		case <-time.After(10 * time.Second):
			t.Fatal("marker frames were not all delivered")
		}
	}
	return delivered
}

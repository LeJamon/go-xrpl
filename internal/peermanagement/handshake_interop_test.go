//go:build cgo && docker

package peermanagement

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rippledInteropImage  = "xrpllabsofficial/xrpld:3.3.0"
	rippledInteropConfig = `[server]
port_rpc_admin_local
port_peer

[port_rpc_admin_local]
port=5005
ip=127.0.0.1
admin=127.0.0.1
protocol=http

[port_peer]
port=51235
ip=0.0.0.0
protocol=peer

[node_db]
type=NuDB
path=/var/lib/xrpld/db/nudb

[database_path]
/var/lib/xrpld/db

[debug_logfile]
/var/log/xrpld/debug.log

[node_size]
tiny

[peers_max]
21

[peer_private]
0

[ips]
127.0.0.1 1

[network_id]
1

[ssl_verify]
0
`
)

type rippledInteropNode struct {
	cid  string
	addr string
}

func startRippledInterop(t *testing.T) *rippledInteropNode {
	t.Helper()

	cid := startRippledInteropContainer(t, rippledInteropConfig, "-p", "0:51235")

	portOutput, err := exec.Command("docker", "port", cid, "51235").Output()
	require.NoError(t, err)
	host, port := parseDockerPort(t, string(portOutput))
	addr := net.JoinHostPort(host, port)
	waitForRippledPeerPort(t, cid, addr)
	return &rippledInteropNode{cid: cid, addr: addr}
}

func TestHandshake_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP not set")
	}

	node := startRippledInterop(t)

	id, err := NewIdentity()
	require.NoError(t, err)
	certPEM, keyPEM, err := id.TLSCertificatePEM()
	require.NoError(t, err)
	endpoint, err := ParseEndpoint(node.addr)
	require.NoError(t, err)

	peer := NewPeer(1, endpoint, false, id, make(chan Event, 1))
	peer.handshakeCfg = DefaultHandshakeConfig()
	peer.handshakeCfg.UserAgent = "goXRPL/interop-test"
	peer.handshakeCfg.NetworkID = 1
	t.Cleanup(func() { _ = peer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, peer.Connect(ctx, PeerConfig{
		PeerTLSConfig: &peertls.Config{CertPEM: certPEM, KeyPEM: keyPEM},
	}))
	assert.Equal(t, PeerStateConnected, peer.State())
	assert.NotNil(t, peer.RemotePublicKey())
	assert.Equal(t, "1", peer.Info().NetworkID)
	assert.Contains(t, peer.Info().Version, "3.3.0")
}

func TestHandshake_Interop_RippledDocker_RippledClient(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP not set")
	}

	overlay, err := New(
		WithListenAddr("0.0.0.0:0"),
		WithDataDir(t.TempDir()),
		WithNetworkID(1),
		WithMaxPeers(1),
		WithMaxInbound(1),
		WithMaxOutbound(0),
		WithPrivateMode(true),
		WithCompression(false),
	)
	require.NoError(t, err)
	connected := make(chan PeerID, 1)
	overlay.SetPeerConnectCallback(func(peerID PeerID) {
		select {
		case connected <- peerID:
		default:
		}
	})

	runDone := make(chan error, 1)
	go func() { runDone <- overlay.Run(context.Background()) }()
	t.Cleanup(func() {
		_ = overlay.Stop()
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("overlay did not stop")
		}
	})
	select {
	case <-overlay.ListenerReady():
	case err := <-runDone:
		runDone <- err
		t.Fatalf("overlay stopped before listener became ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("overlay listener did not become ready")
	}

	_, port, err := net.SplitHostPort(overlay.ListenAddr())
	require.NoError(t, err)
	config := strings.Replace(rippledInteropConfig, "[peer_private]\n0", "[peer_private]\n1", 1)
	config = strings.Replace(
		config,
		"[ips]\n127.0.0.1 1",
		"[ips_fixed]\nhost.docker.internal "+port,
		1,
	)
	cid := startRippledInteropContainer(
		t,
		config,
		"--add-host", "host.docker.internal:host-gateway",
	)
	waitForRippledPeerPort(t, cid, overlay.ListenAddr())

	select {
	case peerID := <-connected:
		peer, ok := overlay.getPeer(peerID)
		require.True(t, ok)
		info := peer.Info()
		assert.True(t, info.Inbound)
		assert.Equal(t, "1", info.NetworkID)
		assert.Contains(t, info.Version, "3.2.0")
		assert.NotEmpty(t, info.PublicKey)
	case err := <-runDone:
		runDone <- err
		t.Fatalf("overlay stopped before rippled connected: %v", err)
	case <-time.After(120 * time.Second):
		logs, _ := exec.Command("docker", "logs", cid).CombinedOutput()
		t.Fatalf("rippled did not connect to Go listener within 120s; logs:\n%s", logs)
	}
}

func startRippledInteropContainer(t *testing.T, config string, dockerOptions ...string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "rippled.cfg")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o644))

	var nonce [4]byte
	_, err := rand.Read(nonce[:])
	require.NoError(t, err)
	containerName := "goxrpl-interop-1475-" + hex.EncodeToString(nonce[:])
	args := []string{"run", "-d", "--name", containerName}
	args = append(args, dockerOptions...)
	args = append(args,
		"-v", configPath+":/etc/xrpld/xrpld.cfg:ro",
		rippledInteropImage,
	)
	cmd := exec.Command("docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "docker run: %s", stderr.String())
	cid := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", cid).Run()
	})
	return cid
}

func waitForRippledPeerPort(t *testing.T, cid, addr string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("docker", "logs", cid).CombinedOutput()
		if err == nil && strings.Contains(string(out), "Opened 'port_peer'") {
			return
		}
		time.Sleep(time.Second)
	}
	out, _ := exec.Command("docker", "logs", cid).CombinedOutput()
	t.Fatalf("rippled did not open port_peer within 120s; addr=%s; logs:\n%s", addr, out)
}

func parseDockerPort(t *testing.T, raw string) (host, port string) {
	t.Helper()
	var v6host, v6port string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		hostPort := strings.TrimSpace(line)
		if i := strings.LastIndex(hostPort, "->"); i >= 0 {
			hostPort = strings.TrimSpace(hostPort[i+2:])
		}
		h, p, err := net.SplitHostPort(hostPort)
		if err != nil {
			continue
		}
		if h == "0.0.0.0" || h == "" {
			h = "127.0.0.1"
		}
		if h == "::" {
			v6host, v6port = "::1", p
			continue
		}
		return h, p
	}
	if v6host != "" {
		return v6host, v6port
	}
	t.Fatalf("could not parse docker port output: %q", raw)
	return "", ""
}

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
	rippledInteropImage  = "xrpllabsofficial/xrpld:3.2.0"
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

func TestHandshake_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP not set")
	}

	configPath := filepath.Join(t.TempDir(), "rippled.cfg")
	require.NoError(t, os.WriteFile(configPath, []byte(rippledInteropConfig), 0o644))

	var nonce [4]byte
	_, err := rand.Read(nonce[:])
	require.NoError(t, err)
	containerName := "goxrpl-interop-1303-" + hex.EncodeToString(nonce[:])

	cmd := exec.Command(
		"docker", "run", "-d",
		"-p", "0:51235",
		"--name", containerName,
		"-v", configPath+":/etc/xrpld/xrpld.cfg:ro",
		rippledInteropImage,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "docker run: %s", stderr.String())
	cid := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", cid).Run()
	})

	portOutput, err := exec.Command("docker", "port", cid, "51235").Output()
	require.NoError(t, err)
	host, port := parseDockerPort(t, string(portOutput))
	addr := net.JoinHostPort(host, port)
	waitForRippledPeerPort(t, cid, addr)

	id, err := NewIdentity()
	require.NoError(t, err)
	certPEM, keyPEM, err := id.TLSCertificatePEM()
	require.NoError(t, err)
	endpoint, err := ParseEndpoint(addr)
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
	assert.Contains(t, peer.Info().Version, "3.2.0")
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

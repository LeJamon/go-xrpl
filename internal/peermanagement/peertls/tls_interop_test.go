//go:build cgo && docker

package peertls

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHandshake_Interop_RippledDocker connects to a rippled instance running
// in a docker container, runs the XRPL HTTP-Upgrade handshake, and asserts
// 101 Switching Protocols. Skipped unless PEERTLS_DOCKER_INTEROP=1.
func TestHandshake_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP not set")
	}

	image := os.Getenv("RIPPLED_IMAGE")
	if image == "" {
		image = "xrpllabsofficial/xrpld:latest"
	}

	cidBytes, err := exec.Command("docker", "run", "-d",
		"-p", "0:51235",
		"--name", "peertls-interop-269",
		image,
	).Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	cid := strings.TrimSpace(string(cidBytes))
	defer exec.Command("docker", "rm", "-f", cid).Run()

	// Discover the host port docker bound for 51235.
	portBytes, err := exec.Command("docker", "port", cid, "51235").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	host, port := parseDockerPort(t, string(portBytes))

	// Wait for rippled to start listening (≤30s).
	addr := net.JoinHostPort(host, port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(time.Second)
	}

	cert, key := generateTestCert(t)

	tcp, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial rippled: %v", err)
	}
	defer tcp.Close()

	pc, err := Client(tcp, &Config{CertPEM: cert, KeyPEM: key})
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	defer pc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pc.HandshakeContext(ctx); err != nil {
		t.Fatalf("HandshakeContext: %v", err)
	}

	if _, err := pc.SharedValue(); err != nil {
		t.Fatalf("SharedValue: %v", err)
	}

	// Send a minimal Upgrade request and read the response status line —
	// we don't need to parse headers fully, just confirm 101.
	req := "GET / HTTP/1.1\r\n" +
		"Upgrade: XRPL/2.2\r\n" +
		"Connection: Upgrade\r\n" +
		"Connect-As: Peer\r\n" +
		"\r\n"
	if _, err := pc.Write([]byte(req)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	br := bufio.NewReader(pc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("got status %d, want 101", resp.StatusCode)
	}
}

func parseDockerPort(t *testing.T, raw string) (host, port string) {
	t.Helper()
	// docker port output: "51235/tcp -> 0.0.0.0:32812\n"
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.Split(line, "->")
		if len(parts) != 2 {
			continue
		}
		hp := strings.TrimSpace(parts[1])
		h, p, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if h == "0.0.0.0" || h == "" {
			h = "127.0.0.1"
		}
		return h, p
	}
	t.Fatalf("could not parse docker port output: %q", raw)
	return "", ""
}

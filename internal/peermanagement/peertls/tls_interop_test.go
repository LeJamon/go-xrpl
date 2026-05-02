//go:build cgo && docker

package peertls

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/protocol"
)

// nodePublicKeyPrefix is the base58 prefix byte for XRPL node public
// keys (results in 'n' prefix). Mirrors peermanagement.NodePublicKeyPrefix.
const nodePublicKeyPrefix = 0x1C

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

	// Suffix the container name with random hex so concurrent test
	// runs (e.g. `go test -count N`, parallel CI shards) don't collide
	// on a fixed name.
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	containerName := "peertls-interop-269-" + hex.EncodeToString(nonce[:])

	cidBytes, err := exec.Command("docker", "run", "-d",
		"-p", "0:51235",
		"--name", containerName,
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

	// Wait for rippled to actually bind the peer port. A plain TCP
	// probe is unreliable on Docker Desktop (the userland proxy accepts
	// connections before rippled is bound), so we look for the explicit
	// "Opened 'port_peer'" log line. Allow up to 120 s — fresh boot
	// under amd64 emulation on arm64 hosts can take ~30-60 s before
	// rippled finishes loading manifests and opens the listener.
	addr := net.JoinHostPort(host, port)
	{
		ready := false
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			out, err := exec.Command("docker", "logs", cid).CombinedOutput()
			if err == nil && strings.Contains(string(out), "Opened 'port_peer'") {
				ready = true
				break
			}
			time.Sleep(time.Second)
		}
		if !ready {
			t.Fatalf("rippled did not open port_peer within 120s; addr=%s", addr)
		}
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

	sharedValue, err := pc.SharedValue()
	if err != nil {
		t.Fatalf("SharedValue: %v", err)
	}

	// Construct a full XRPL handshake request: rippled requires a
	// Public-Key header and a Session-Signature over the SharedValue,
	// otherwise verifyHandshake throws and the response is 400. We sign
	// with an ephemeral secp256k1 key so the test does not leak per-run
	// state. Same scheme as peermanagement.Identity but inlined here to
	// keep peertls free of a peermanagement import cycle.
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("NewPrivateKey: %v", err)
	}
	nodePub := encodeNodePublicKey(priv.PubKey().SerializeCompressed())
	sig := btcecdsa.Sign(priv, sharedValue).Serialize()
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	netTime := strconv.FormatUint(uint64(time.Now().Unix()-protocol.RippleEpochUnix), 10)

	req := "GET / HTTP/1.1\r\n" +
		"User-Agent: peertls-interop-test\r\n" +
		"Upgrade: XRPL/2.2\r\n" +
		"Connection: Upgrade\r\n" +
		"Connect-As: Peer\r\n" +
		"Public-Key: " + nodePub + "\r\n" +
		"Session-Signature: " + sigB64 + "\r\n" +
		"Network-Time: " + netTime + "\r\n" +
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
		t.Fatalf("got status %d, want 101 (resp headers: %v)",
			resp.StatusCode, resp.Header)
	}

	// Symmetric verification: parse rippled's Public-Key + Session-
	// Signature and confirm the signature validates over the same
	// SharedValue. This is the actual interop guarantee — the docker
	// container is running real rippled, so a valid signature here means
	// our SharedValue computation matches rippled's makeSharedValue
	// byte-for-byte.
	peerPubB58 := resp.Header.Get("Public-Key")
	peerSigB64 := resp.Header.Get("Session-Signature")
	if peerPubB58 == "" || peerSigB64 == "" {
		t.Fatalf("rippled response missing Public-Key or Session-Signature")
	}
	peerPubBytes, err := decodeNodePublicKey(peerPubB58)
	if err != nil {
		t.Fatalf("decode peer Public-Key: %v", err)
	}
	peerPub, err := btcec.ParsePubKey(peerPubBytes)
	if err != nil {
		t.Fatalf("parse peer Public-Key: %v", err)
	}
	peerSig, err := base64.StdEncoding.DecodeString(peerSigB64)
	if err != nil {
		t.Fatalf("decode peer Session-Signature: %v", err)
	}
	parsedSig, err := btcecdsa.ParseDERSignature(peerSig)
	if err != nil {
		t.Fatalf("parse peer DER signature: %v", err)
	}
	if !parsedSig.Verify(sharedValue, peerPub) {
		t.Fatalf("rippled Session-Signature did not verify against our SharedValue")
	}
}

// encodeNodePublicKey base58check-encodes a 33-byte compressed
// secp256k1 public key with the XRPL node-public-key prefix. Mirrors
// peermanagement.Identity.EncodedPublicKey.
func encodeNodePublicKey(pubKey []byte) string {
	payload := make([]byte, 1+len(pubKey))
	payload[0] = nodePublicKeyPrefix
	copy(payload[1:], pubKey)
	checksum := doubleSHA256(payload)[:4]
	full := append(payload, checksum...)
	return addresscodec.EncodeBase58(full)
}

// decodeNodePublicKey reverses encodeNodePublicKey, returning the raw
// 33-byte compressed pubkey.
func decodeNodePublicKey(s string) ([]byte, error) {
	data := addresscodec.DecodeBase58(s)
	if len(data) != 1+33+4 {
		return nil, errInvalidNodePubKey
	}
	payload := data[:len(data)-4]
	if payload[0] != nodePublicKeyPrefix {
		return nil, errInvalidNodePubKey
	}
	want := doubleSHA256(payload)[:4]
	got := data[len(data)-4:]
	for i := range want {
		if want[i] != got[i] {
			return nil, errInvalidNodePubKey
		}
	}
	return payload[1:], nil
}

var errInvalidNodePubKey = errors.New("invalid node public key")

func doubleSHA256(data []byte) []byte {
	h := sha256.Sum256(data)
	h2 := sha256.Sum256(h[:])
	return h2[:]
}

func parseDockerPort(t *testing.T, raw string) (host, port string) {
	t.Helper()
	// `docker port <cid> 51235` output is one line per binding. Two
	// supported formats:
	//   legacy:  "51235/tcp -> 0.0.0.0:32812"
	//   modern (docker 23+): "0.0.0.0:55001" (no arrow when the port
	//                        argument is supplied).
	// Prefer IPv4; fall back to whatever parses.
	var v6host, v6port string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		hp := strings.TrimSpace(line)
		if i := strings.LastIndex(hp, "->"); i >= 0 {
			hp = strings.TrimSpace(hp[i+2:])
		}
		h, p, err := net.SplitHostPort(hp)
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

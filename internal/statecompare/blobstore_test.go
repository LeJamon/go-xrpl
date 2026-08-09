package statecompare

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGetBoolEnvStrict(t *testing.T) {
	t.Setenv("TEST_BOOL", "true")
	value, err := getBoolEnv("TEST_BOOL", false)
	if err != nil || !value {
		t.Fatalf("getBoolEnv = %t, %v", value, err)
	}
	t.Setenv("TEST_BOOL", "truthy")
	if _, err := getBoolEnv("TEST_BOOL", false); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}

func TestIdleReadConnTimesOutStalledReads(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	conn := &idleReadConn{Conn: client, timeout: 20 * time.Millisecond}
	_, err := conn.Read(make([]byte, 1))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Read error = %v, want timeout", err)
	}
}

func TestParseBlobKeyGrammar(t *testing.T) {
	valid := map[string]struct {
		kind byte
		seq  uint32
	}{
		"state/ckpt-0.pack":          {kindState, 0},
		"state/ckpt-4294967295.pack": {kindState, ^uint32(0)},
		"ledger/42.pack":             {kindLedger, 42},
	}
	for key, want := range valid {
		kind, seq, err := parseBlobKey(key)
		if err != nil || kind != want.kind || seq != want.seq {
			t.Fatalf("parseBlobKey(%q) = %d,%d,%v", key, kind, seq, err)
		}
	}
	for _, key := range []string{
		"../ledger/1.pack", "ledger/../1.pack", "ledger/01.pack", "ledger/+1.pack",
		"ledger/1.pack/extra", `ledger\1.pack`, "/ledger/1.pack", "state/1.pack",
		"state/ckpt-4294967296.pack", "unknown/1.pack",
	} {
		if _, _, err := parseBlobKey(key); err == nil {
			t.Errorf("parseBlobKey(%q) unexpectedly succeeded", key)
		}
	}
}

func TestLocalBlobStoreConfinesSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "ledger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ledger", "1.pack"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newBlobStore(blobStoreConfig{backend: "local", localRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	object, err := store.open(context.Background(), "ledger/1.pack")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(object)
	if err != nil {
		t.Fatal(err)
	}
	_ = object.Close()
	if string(data) != "ok" || object.size != 2 {
		t.Fatalf("object = %q size=%d", data, object.size)
	}

	if runtime.GOOS == "windows" {
		return
	}
	outside := filepath.Join(t.TempDir(), "outside.pack")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "ledger", "2.pack")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.open(context.Background(), "ledger/2.pack"); err == nil {
		t.Fatal("outside-root symlink accepted")
	}
}

func TestS3ConfigValidation(t *testing.T) {
	valid := blobStoreConfig{
		backend: "s3", endpointURL: "https://example.com", bucket: "valid-bucket",
		accessKey: "access", secretKey: "secret", region: "us-east-1", secure: true,
	}
	if _, err := validateS3Config(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	tests := []func(*blobStoreConfig){
		func(c *blobStoreConfig) { c.endpointURL = "file:///tmp/data" },
		func(c *blobStoreConfig) { c.endpointURL = "https://user@example.com" },
		func(c *blobStoreConfig) { c.endpointURL = "https://example.com/path" },
		func(c *blobStoreConfig) { c.bucket = "127.0.0.1" },
		func(c *blobStoreConfig) { c.accessKey = "" },
		func(c *blobStoreConfig) { c.secure = false },
	}
	for i, mutate := range tests {
		cfg := valid
		mutate(&cfg)
		if _, err := validateS3Config(cfg); err == nil {
			t.Errorf("invalid config %d accepted", i)
		}
	}
}

func TestS3BlobStoreRequiresLengthAndRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bucket/ledger/1.pack":
			w.Header().Set("Location", "/bucket/ledger/2.pack")
			w.WriteHeader(http.StatusFound)
		case "/bucket/ledger/2.pack":
			w.Header().Set("Transfer-Encoding", "chunked")
			_, _ = io.WriteString(w, "data")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	store, err := newS3BlobStore(blobStoreConfig{
		backend: "s3", endpointURL: server.URL, bucket: "bucket", accessKey: "access",
		secretKey: "secret", region: "us-east-1", secure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.open(context.Background(), "ledger/1.pack"); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect error = %v", err)
	}
	if _, err := store.open(context.Background(), "ledger/2.pack"); err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("missing length error = %v", err)
	}
}

func TestContextReadCloserCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &contextReadCloser{ctx: ctx, ReadCloser: io.NopCloser(strings.NewReader("data"))}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v", err)
	}
}

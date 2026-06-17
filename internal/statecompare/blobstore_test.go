package statecompare

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetBoolEnv(t *testing.T) {
	const key = "STATECOMPARE_TEST_BOOL"
	cases := map[string]bool{
		"1": true, "true": true, "yes": true, "on": true,
		"TRUE": true, " On ": true,
		"0": false, "false": false, "no": false, "off": false, "bogus": false,
	}
	for raw, want := range cases {
		t.Setenv(key, raw)
		if got := getBoolEnv(key, false); got != want {
			t.Errorf("getBoolEnv(%q) = %v, want %v", raw, got, want)
		}
	}
	t.Setenv(key, "")
	if got := getBoolEnv(key, true); got != true {
		t.Errorf("getBoolEnv(empty) = %v, want default true", got)
	}
}

func TestBlobStoreConfigFromEnvDefaults(t *testing.T) {
	for _, k := range []string{
		"BLOBSTORE_BACKEND", "MINIO_ENDPOINT_URL", "MINIO_ACCESS_KEY",
		"MINIO_SECRET_KEY", "MINIO_BUCKET", "MINIO_REGION", "MINIO_SECURE",
		"BLOBSTORE_LOCAL_ROOT",
	} {
		t.Setenv(k, "")
	}
	want := BlobStoreConfig{
		Backend:     "local",
		EndpointURL: "http://localhost:9000",
		AccessKey:   "minioadmin",
		SecretKey:   "minioadmin",
		Bucket:      "xrpl-replay",
		Region:      "us-east-1",
		Secure:      false,
		LocalRoot:   "./.blobstore",
	}
	if got := BlobStoreConfigFromEnv(); got != want {
		t.Errorf("BlobStoreConfigFromEnv() = %+v, want %+v", got, want)
	}
}

func TestBlobStoreConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("BLOBSTORE_BACKEND", "S3")
	t.Setenv("MINIO_ENDPOINT_URL", "https://minio.example.com")
	t.Setenv("MINIO_ACCESS_KEY", "ak")
	t.Setenv("MINIO_SECRET_KEY", "sk")
	t.Setenv("MINIO_BUCKET", "packs")
	t.Setenv("MINIO_REGION", "eu-west-1")
	t.Setenv("MINIO_SECURE", "true")
	t.Setenv("BLOBSTORE_LOCAL_ROOT", "/data")

	want := BlobStoreConfig{
		Backend:     "s3", // lowercased/trimmed
		EndpointURL: "https://minio.example.com",
		AccessKey:   "ak",
		SecretKey:   "sk",
		Bucket:      "packs",
		Region:      "eu-west-1",
		Secure:      true,
		LocalRoot:   "/data",
	}
	if got := BlobStoreConfigFromEnv(); got != want {
		t.Errorf("BlobStoreConfigFromEnv() = %+v, want %+v", got, want)
	}
}

func TestNewBlobStoreUnknownBackend(t *testing.T) {
	if _, err := newBlobStore(BlobStoreConfig{Backend: "azure"}); err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
}

func TestLocalBlobStoreGet(t *testing.T) {
	root := t.TempDir()
	store := &localBlobStore{root: root}

	blob := packState(7, []StateEntry{{Index: idx(0x01), Data: []byte("x")}})
	key := "state/ckpt-7.pack"
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(key)), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := store.get(context.Background(), key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("get returned %d bytes, want %d", len(got), len(blob))
	}

	if _, err := store.get(context.Background(), "state/missing.pack"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
}

func TestLocalBlobStoreGetReader(t *testing.T) {
	root := t.TempDir()
	store := &localBlobStore{root: root}

	entries := []StateEntry{{Index: idx(0x01), Data: []byte("x")}, {Index: idx(0x02), Data: []byte("yz")}}
	blob := packState(7, entries)
	key := "state/ckpt-7.pack"
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(key)), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := store.getReader(context.Background(), key)
	if err != nil {
		t.Fatalf("getReader: %v", err)
	}
	defer r.Close()

	got := 0
	seq, _, err := unpackStateStream(r, func([32]byte, []byte) error {
		got++
		return nil
	})
	if err != nil {
		t.Fatalf("unpackStateStream: %v", err)
	}
	if seq != 7 || got != len(entries) {
		t.Errorf("seq=%d entries=%d, want seq=7 entries=%d", seq, got, len(entries))
	}

	if _, err := store.getReader(context.Background(), "state/missing.pack"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
}

// TestSignV4GoldenVector verifies the SigV4 signer against the documented AWS
// "GET Object" example, which fixes the expected signature for a known
// request. Matching it proves the canonical request, string-to-sign and
// signing-key derivation are all correct.
func TestSignV4GoldenVector(t *testing.T) {
	const (
		accessKey = "AKIAIOSFODNN7EXAMPLE"
		secretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		wantSig   = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
		wantSH    = "host;range;x-amz-content-sha256;x-amz-date"
	)
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")

	when := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	signV4(req, emptyPayloadSHA256, when, "us-east-1", accessKey, secretKey)

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "Signature="+wantSig) {
		t.Errorf("Authorization = %q\nwant Signature=%s", auth, wantSig)
	}
	if !strings.Contains(auth, "SignedHeaders="+wantSH) {
		t.Errorf("Authorization = %q\nwant SignedHeaders=%s", auth, wantSH)
	}
	if req.Header.Get("x-amz-date") != "20130524T000000Z" {
		t.Errorf("x-amz-date = %q, want 20130524T000000Z", req.Header.Get("x-amz-date"))
	}
	if req.Header.Get("x-amz-content-sha256") != emptyPayloadSHA256 {
		t.Errorf("x-amz-content-sha256 = %q, want empty-payload hash", req.Header.Get("x-amz-content-sha256"))
	}
}

func TestS3BlobStoreGet(t *testing.T) {
	blob := packState(1, []StateEntry{{Index: idx(0x01), Data: []byte("x")}})
	const wantPath = "/xrpl-replay/ledger/1000.pack"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("missing/bad Authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-amz-content-sha256") == "" {
			t.Error("missing x-amz-content-sha256 header")
		}
		switch r.URL.Path {
		case wantPath:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(blob)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store, err := newS3BlobStore(BlobStoreConfig{
		Backend:     "s3",
		EndpointURL: srv.URL,
		AccessKey:   "ak",
		SecretKey:   "sk",
		Bucket:      "xrpl-replay",
		Region:      "us-east-1",
	})
	if err != nil {
		t.Fatalf("newS3BlobStore: %v", err)
	}

	got, err := store.get(context.Background(), "ledger/1000.pack")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("get returned %d bytes, want %d", len(got), len(blob))
	}

	if _, err := store.get(context.Background(), "ledger/missing.pack"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
}

func TestS3BlobStoreGetReader(t *testing.T) {
	entries := []StateEntry{{Index: idx(0x01), Data: []byte("x")}, {Index: idx(0xaa), Data: []byte("abc")}}
	blob := packState(99250000, entries)
	const wantPath = "/xrpl-replay/state/ckpt-99250000.pack"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("missing/bad Authorization header: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case wantPath:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(blob)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store, err := newS3BlobStore(BlobStoreConfig{
		Backend:     "s3",
		EndpointURL: srv.URL,
		AccessKey:   "ak",
		SecretKey:   "sk",
		Bucket:      "xrpl-replay",
		Region:      "us-east-1",
	})
	if err != nil {
		t.Fatalf("newS3BlobStore: %v", err)
	}

	r, err := store.getReader(context.Background(), "state/ckpt-99250000.pack")
	if err != nil {
		t.Fatalf("getReader: %v", err)
	}
	defer r.Close()

	got := 0
	seq, _, err := unpackStateStream(r, func([32]byte, []byte) error {
		got++
		return nil
	})
	if err != nil {
		t.Fatalf("unpackStateStream: %v", err)
	}
	if seq != 99250000 || got != len(entries) {
		t.Errorf("seq=%d entries=%d, want seq=99250000 entries=%d", seq, got, len(entries))
	}

	if _, err := store.getReader(context.Background(), "state/missing.pack"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
}

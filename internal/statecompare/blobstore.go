package statecompare

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// blobStore reads the immutable pack objects the lab writes to object storage.
// Two backends mirror the lab's core/blobstore.py: a local directory for
// dev/test, and S3/MinIO for production cross-pod blob sharing.
type blobStore interface {
	get(ctx context.Context, key string) ([]byte, error)
	// getReader returns the object as a stream so a large pack is consumed
	// incrementally rather than buffered whole. The caller owns the reader and
	// must Close it.
	getReader(ctx context.Context, key string) (io.ReadCloser, error)
}

// BlobStoreConfig selects and configures the blob backend. Field defaults and
// env-var names match the lab's BlobStoreConfig so the same deployment env
// drives both the Python workers and this Go reader.
type BlobStoreConfig struct {
	Backend     string // "local", "s3", or "minio"
	EndpointURL string
	AccessKey   string
	SecretKey   string
	Bucket      string
	Region      string
	Secure      bool
	LocalRoot   string
}

// BlobStoreConfigFromEnv builds a BlobStoreConfig from the environment.
func BlobStoreConfigFromEnv() BlobStoreConfig {
	return BlobStoreConfig{
		Backend:     strings.ToLower(strings.TrimSpace(getEnvOrDefault("BLOBSTORE_BACKEND", "local"))),
		EndpointURL: getEnvOrDefault("MINIO_ENDPOINT_URL", "http://localhost:9000"),
		AccessKey:   getEnvOrDefault("MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey:   getEnvOrDefault("MINIO_SECRET_KEY", "minioadmin"),
		Bucket:      getEnvOrDefault("MINIO_BUCKET", "xrpl-replay"),
		Region:      getEnvOrDefault("MINIO_REGION", "us-east-1"),
		Secure:      getBoolEnv("MINIO_SECURE", false),
		LocalRoot:   getEnvOrDefault("BLOBSTORE_LOCAL_ROOT", "./.blobstore"),
	}
}

// getBoolEnv parses a truthy env var the same way the lab's _bool helper does.
func getBoolEnv(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return defaultValue
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// newBlobStore constructs the backend selected by cfg.Backend.
func newBlobStore(cfg BlobStoreConfig) (blobStore, error) {
	switch cfg.Backend {
	case "local":
		return &localBlobStore{root: cfg.LocalRoot}, nil
	case "s3", "minio":
		return newS3BlobStore(cfg)
	default:
		return nil, fmt.Errorf("statecompare: unknown blobstore backend %q", cfg.Backend)
	}
}

// localBlobStore reads packs from a directory tree, needing no running service.
type localBlobStore struct {
	root string
}

func (l *localBlobStore) get(_ context.Context, key string) ([]byte, error) {
	path := filepath.Join(l.root, filepath.FromSlash(key))
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("blob %q: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("reading blob %q: %w", key, err)
	}
	return data, nil
}

func (l *localBlobStore) getReader(_ context.Context, key string) (io.ReadCloser, error) {
	path := filepath.Join(l.root, filepath.FromSlash(key))
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("blob %q: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("reading blob %q: %w", key, err)
	}
	return f, nil
}

// s3BlobStore fetches packs from S3/MinIO over plain HTTP, signing each GET
// with AWS Signature Version 4. Path-style addressing (the MinIO default) is
// used: the object URL is <endpoint>/<bucket>/<key>.
type s3BlobStore struct {
	endpoint  *url.URL
	bucket    string
	accessKey string
	secretKey string
	region    string
	client    *http.Client
}

func newS3BlobStore(cfg BlobStoreConfig) (*s3BlobStore, error) {
	ep := cfg.EndpointURL
	if !strings.Contains(ep, "://") {
		scheme := "http"
		if cfg.Secure {
			scheme = "https"
		}
		ep = scheme + "://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return nil, fmt.Errorf("statecompare: invalid MINIO_ENDPOINT_URL %q: %w", cfg.EndpointURL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("statecompare: MINIO_ENDPOINT_URL %q has no host", cfg.EndpointURL)
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	return &s3BlobStore{
		endpoint:  u,
		bucket:    cfg.Bucket,
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		region:    region,
		// No client-wide timeout: packs are large and the caller's context
		// governs the deadline. The default transport handles connection reuse.
		client: &http.Client{},
	}, nil
}

// emptyPayloadSHA256 is the SHA-256 of an empty body, used as the signed
// content hash for an unsigned-payload GET.
const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *s3BlobStore) get(ctx context.Context, key string) ([]byte, error) {
	r, err := s.getReader(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading blob %q: %w", key, err)
	}
	return data, nil
}

func (s *s3BlobStore) getReader(ctx context.Context, key string) (io.ReadCloser, error) {
	reqURL := *s.endpoint
	reqURL.Path = "/" + s.bucket + "/" + key

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request for blob %q: %w", key, err)
	}
	req.Host = s.endpoint.Host
	signV4(req, emptyPayloadSHA256, time.Now().UTC(), s.region, s.accessKey, s.secretKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching blob %q: %w", key, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("blob %q: %w", key, ErrNotFound)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("fetching blob %q: status %s: %s", key, resp.Status, strings.TrimSpace(string(body)))
	}
}

// signV4 signs req in place with AWS Signature Version 4 for service "s3",
// setting the x-amz-date, x-amz-content-sha256 and Authorization headers. It
// signs the Host header plus every header already present on req, so callers
// that set extra headers (e.g. Range) get them covered.
func signV4(req *http.Request, payloadSHA256 string, t time.Time, region, accessKey, secretKey string) {
	const (
		algorithm = "AWS4-HMAC-SHA256"
		service   = "s3"
	)
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadSHA256)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	// Canonical + signed headers: host plus everything currently on the
	// request, lowercased and sorted by name.
	headers := map[string]string{"host": host}
	for name, vals := range req.Header {
		headers[strings.ToLower(name)] = strings.TrimSpace(strings.Join(vals, ","))
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headers[name])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.Path),
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadSHA256,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hex.EncodeToString(sha256Sum([]byte(canonicalRequest))),
	}, "\n")

	signingKey := hmacSHA256(
		hmacSHA256(
			hmacSHA256(
				hmacSHA256([]byte("AWS4"+secretKey), dateStamp),
				region),
			service),
		"aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, accessKey, scope, signedHeaders, signature,
	))
}

// canonicalURI URI-encodes each path segment per RFC 3986 while preserving the
// '/' separators, as S3's SigV4 canonical URI requires.
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = uriEncode(seg)
	}
	return strings.Join(segments, "/")
}

// uriEncode percent-encodes a single path segment, leaving the RFC 3986
// unreserved set untouched (S3 also treats '/' specially, handled by the
// caller).
func uriEncode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

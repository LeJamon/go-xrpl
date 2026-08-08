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
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type blobObject struct {
	io.ReadCloser
	size int64
}

type blobStore interface {
	open(ctx context.Context, key string) (*blobObject, error)
	Close() error
}

type blobStoreConfig struct {
	backend     string
	endpointURL string
	accessKey   string
	secretKey   string
	bucket      string
	region      string
	secure      bool
	localRoot   string
}

func blobStoreConfigFromEnv() (blobStoreConfig, error) {
	secure, err := getBoolEnv("MINIO_SECURE", false)
	if err != nil {
		return blobStoreConfig{}, err
	}
	return blobStoreConfig{
		backend:     strings.ToLower(strings.TrimSpace(getEnvOrDefault("BLOBSTORE_BACKEND", "local"))),
		endpointURL: getEnvOrDefault("MINIO_ENDPOINT_URL", "http://localhost:9000"),
		accessKey:   getEnvOrDefault("MINIO_ACCESS_KEY", "minioadmin"),
		secretKey:   getEnvOrDefault("MINIO_SECRET_KEY", "minioadmin"),
		bucket:      getEnvOrDefault("MINIO_BUCKET", "xrpl-replay"),
		region:      getEnvOrDefault("MINIO_REGION", "us-east-1"),
		secure:      secure,
		localRoot:   getEnvOrDefault("BLOBSTORE_LOCAL_ROOT", "./.blobstore"),
	}, nil
}

func getBoolEnv(key string, defaultValue bool) (bool, error) {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return defaultValue, nil
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("statecompare: %s must be a boolean, got %q", key, os.Getenv(key))
	}
}

func newBlobStore(cfg blobStoreConfig) (blobStore, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.backend)) {
	case "local":
		if strings.TrimSpace(cfg.localRoot) == "" {
			return nil, errors.New("statecompare: BLOBSTORE_LOCAL_ROOT is empty")
		}
		root, err := os.OpenRoot(cfg.localRoot)
		if err != nil {
			return nil, fmt.Errorf("statecompare: opening blobstore root %q: %w", cfg.localRoot, err)
		}
		return &localBlobStore{root: root}, nil
	case "s3", "minio":
		return newS3BlobStore(cfg)
	default:
		return nil, fmt.Errorf("statecompare: unknown blobstore backend %q", cfg.backend)
	}
}

type localBlobStore struct {
	root *os.Root
}

func (l *localBlobStore) open(ctx context.Context, key string) (*blobObject, error) {
	if _, _, err := parseBlobKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := l.root.Open(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("blob %q: %w", key, errNotFound)
		}
		return nil, fmt.Errorf("opening blob %q: %w", key, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stating blob %q: %w", key, err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("blob %q is not a regular file", key), file.Close())
	}
	return &blobObject{
		ReadCloser: &contextReadCloser{ctx: ctx, ReadCloser: file},
		size:       info.Size(),
	}, nil
}

func (l *localBlobStore) Close() error {
	return l.root.Close()
}

type contextReadCloser struct {
	ctx context.Context
	io.ReadCloser
}

func (r *contextReadCloser) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

type s3BlobStore struct {
	endpoint  *url.URL
	bucket    string
	accessKey string
	secretKey string
	region    string
	client    *http.Client
}

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
)

func newS3BlobStore(cfg blobStoreConfig) (*s3BlobStore, error) {
	u, err := validateS3Config(cfg)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	return &s3BlobStore{
		endpoint:  u,
		bucket:    cfg.bucket,
		accessKey: cfg.accessKey,
		secretKey: cfg.secretKey,
		region:    cfg.region,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validateS3Config(cfg blobStoreConfig) (*url.URL, error) {
	endpoint := strings.TrimSpace(cfg.endpointURL)
	if endpoint == "" {
		return nil, errors.New("statecompare: MINIO_ENDPOINT_URL is empty")
	}
	if !strings.Contains(endpoint, "://") {
		scheme := "http"
		if cfg.secure {
			scheme = "https"
		}
		endpoint = scheme + "://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("statecompare: invalid MINIO_ENDPOINT_URL %q: %w", cfg.endpointURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("statecompare: MINIO_ENDPOINT_URL scheme %q is not http or https", u.Scheme)
	}
	if (u.Scheme == "https") != cfg.secure {
		return nil, fmt.Errorf("statecompare: MINIO_SECURE=%t conflicts with endpoint scheme %q", cfg.secure, u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("statecompare: MINIO_ENDPOINT_URL %q has no host", cfg.endpointURL)
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("statecompare: MINIO_ENDPOINT_URL %q must not contain credentials, path, query, or fragment", cfg.endpointURL)
	}
	if !validBucket(cfg.bucket) {
		return nil, fmt.Errorf("statecompare: invalid MINIO_BUCKET %q", cfg.bucket)
	}
	if strings.TrimSpace(cfg.accessKey) == "" || strings.TrimSpace(cfg.secretKey) == "" {
		return nil, errors.New("statecompare: MINIO_ACCESS_KEY and MINIO_SECRET_KEY must be non-empty")
	}
	if !regionPattern.MatchString(cfg.region) {
		return nil, fmt.Errorf("statecompare: invalid MINIO_REGION %q", cfg.region)
	}
	u.Path = ""
	return u, nil
}

func validBucket(bucket string) bool {
	if !bucketPattern.MatchString(bucket) || strings.Contains(bucket, "..") ||
		strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") {
		return false
	}
	return net.ParseIP(bucket) == nil
}

const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *s3BlobStore) open(ctx context.Context, key string) (*blobObject, error) {
	if _, _, err := parseBlobKey(key); err != nil {
		return nil, err
	}
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
		if resp.ContentLength < 0 {
			return nil, errors.Join(fmt.Errorf("fetching blob %q: response has no Content-Length", key), resp.Body.Close())
		}
		return &blobObject{ReadCloser: resp.Body, size: resp.ContentLength}, nil
	case http.StatusNotFound:
		return nil, errors.Join(fmt.Errorf("blob %q: %w", key, errNotFound), resp.Body.Close())
	default:
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
		closeErr := resp.Body.Close()
		return nil, errors.Join(
			fmt.Errorf("fetching blob %q: status %s: %s", key, resp.Status, strings.TrimSpace(string(body))),
			readErr,
			closeErr,
		)
	}
}

func (s *s3BlobStore) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

func parseBlobKey(key string) (kind byte, seq uint32, err error) {
	parts := strings.Split(key, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(key, "\\") {
		return 0, 0, fmt.Errorf("statecompare: invalid blob key %q", key)
	}
	name := parts[1]
	var number string
	switch parts[0] {
	case "state":
		kind = kindState
		if !strings.HasPrefix(name, "ckpt-") || !strings.HasSuffix(name, ".pack") {
			return 0, 0, fmt.Errorf("statecompare: invalid state blob key %q", key)
		}
		number = strings.TrimSuffix(strings.TrimPrefix(name, "ckpt-"), ".pack")
	case "ledger":
		kind = kindLedger
		if !strings.HasSuffix(name, ".pack") {
			return 0, 0, fmt.Errorf("statecompare: invalid ledger blob key %q", key)
		}
		number = strings.TrimSuffix(name, ".pack")
	default:
		return 0, 0, fmt.Errorf("statecompare: invalid blob key prefix %q", parts[0])
	}
	value, parseErr := strconv.ParseUint(number, 10, 32)
	if parseErr != nil || strconv.FormatUint(value, 10) != number {
		return 0, 0, fmt.Errorf("statecompare: invalid blob key sequence in %q", key)
	}
	return kind, uint32(value), nil
}

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
	headers := map[string]string{"host": host}
	for name, values := range req.Header {
		headers[strings.ToLower(name)] = strings.TrimSpace(strings.Join(values, ","))
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

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = uriEncode(segment)
	}
	return strings.Join(segments, "/")
}

func uriEncode(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if c := value[i]; strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", value[i])
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
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

package list

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// DefaultRefreshInterval matches rippled's
// ValidatorSite::default_refresh_interval — 5 minutes between polls of
// a configured publisher URL.
const defaultRefreshInterval = 5 * time.Minute

// DefaultMinRefresh is the floor applied to a publisher-supplied
// refresh interval. One minute matches rippled's clamp at
// ValidatorSite.cpp:486.
const defaultMinRefresh = 1 * time.Minute

// DefaultMaxRefresh is the ceiling applied to a publisher-supplied
// refresh interval. 24 hours matches rippled.
const defaultMaxRefresh = 24 * time.Hour

// DefaultRequestTimeout caps a single HTTP fetch attempt. Mirrors
// rippled's ValidatorSite constructor default at
// rippled/src/xrpld/app/misc/ValidatorSite.h:142-145
// (`std::chrono::seconds timeout = std::chrono::seconds{20}`).
const defaultRequestTimeout = 20 * time.Second

// MaxRedirects caps the number of HTTP redirects followed during a
// single fetch. Matches rippled ValidatorSite.cpp:36 `max_redirects = 3`.
const maxRedirects = 3

// ErrorRetryInterval is the cadence used to retry a failed fetch.
// Mirrors rippled's error_retry_interval at ValidatorSite.cpp:35.
const errorRetryInterval = 30 * time.Second

// missingFieldsMessage mirrors rippled's exception text from
// ValidatorSite.cpp::parseJsonResponse ("Missing fields in JSON
// response") so external monitors keyed on the literal string match.
const missingFieldsMessage = "Missing fields in JSON response"
const missingFieldsStatusMessage = "missing fields"

type envelope struct {
	Object         bool            `json:"-"`
	Manifest       envelopeString  `json:"manifest"`
	PublicKey      json.RawMessage `json:"public_key,omitempty"`
	Blob           envelopeString  `json:"blob"`
	Signature      envelopeString  `json:"signature"`
	Version        envelopeUint32  `json:"version"`
	BlobsV2        envelopeBlobs   `json:"blobs_v2"`
	RefreshMinutes envelopeNumber  `json:"refresh_interval,omitempty"`
}

func (e *envelope) UnmarshalJSON(data []byte) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		*e = envelope{}
		return nil
	}
	type envelopeFields envelope
	var decoded envelopeFields
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = envelope(decoded)
	e.Object = true
	return nil
}

// envelopeBlob is a v2 collection entry inside the envelope.
type envelopeBlob struct {
	Manifest  envelopeString `json:"manifest"`
	Blob      envelopeString `json:"blob"`
	Signature envelopeString `json:"signature"`
}

type envelopeString struct {
	Value   string
	Present bool
	Valid   bool
}

func (s *envelopeString) UnmarshalJSON(data []byte) error {
	*s = envelopeString{Present: true}
	if string(data) == "null" {
		return nil
	}
	if err := json.Unmarshal(data, &s.Value); err != nil {
		return nil
	}
	s.Valid = true
	return nil
}

type envelopeUint32 struct {
	Value   uint32
	Present bool
	Valid   bool
}

func (v *envelopeUint32) UnmarshalJSON(data []byte) error {
	*v = envelopeUint32{Present: true}
	if string(data) == "null" {
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil || value < 0 || value > 2147483647 {
		return nil
	}
	v.Value = uint32(value)
	v.Valid = true
	return nil
}

type envelopeBlobs struct {
	Values  []envelopeBlob
	Present bool
	Valid   bool
}

type envelopeNumber struct {
	Value float64
	Valid bool
}

func (n *envelopeNumber) UnmarshalJSON(data []byte) error {
	*n = envelopeNumber{}
	if err := json.Unmarshal(data, &n.Value); err != nil {
		return nil
	}
	n.Valid = true
	return nil
}

func (b *envelopeBlobs) UnmarshalJSON(data []byte) error {
	*b = envelopeBlobs{Present: true}
	if string(data) == "null" {
		return nil
	}
	if err := json.Unmarshal(data, &b.Values); err != nil {
		return nil
	}
	b.Valid = true
	return nil
}

// toCollection builds the v2 collection view of the envelope for the
// shared ApplyCollection apply path. Each entry carries its embedded
// manifest when present; ApplyCollection falls back to the envelope-level
// shared manifest otherwise.
func (e *envelope) toCollection() *message.ValidatorListCollection {
	coll := &message.ValidatorListCollection{
		Version:  e.Version.Value,
		Manifest: []byte(e.Manifest.Value),
	}
	if !e.BlobsV2.Present {
		return coll
	}
	for _, b := range e.BlobsV2.Values {
		var localManifest []byte
		if b.Manifest.Present {
			// Allocate even for an explicitly empty string: message
			// ValidatorBlobInfo uses nil-vs-present to distinguish an
			// omitted override from an explicit empty override.
			localManifest = make([]byte, len(b.Manifest.Value))
			copy(localManifest, b.Manifest.Value)
		}
		coll.Blobs = append(coll.Blobs, message.ValidatorBlobInfo{
			Manifest:  localManifest,
			Blob:      []byte(b.Blob.Value),
			Signature: []byte(b.Signature.Value),
		})
	}
	return coll
}

func (e *envelope) validateShape() error {
	if !e.Object || !e.Version.Present || !e.Version.Valid || !e.Manifest.Present || !e.Manifest.Valid {
		return errors.New(missingFieldsMessage)
	}
	if e.Version.Value == 1 {
		if !e.Blob.Present || !e.Blob.Valid || !e.Signature.Present || !e.Signature.Valid || e.BlobsV2.Present {
			return errors.New(missingFieldsMessage)
		}
		return nil
	}
	if !e.BlobsV2.Present || !e.BlobsV2.Valid || len(e.BlobsV2.Values) == 0 || len(e.BlobsV2.Values) > 5 || e.Blob.Present || e.Signature.Present {
		return errors.New(missingFieldsMessage)
	}
	for _, blob := range e.BlobsV2.Values {
		if !blob.Blob.Present || !blob.Blob.Valid || !blob.Signature.Present || !blob.Signature.Valid {
			return errors.New(missingFieldsMessage)
		}
		if blob.Manifest.Present && !blob.Manifest.Valid {
			return errors.New(missingFieldsMessage)
		}
	}
	return nil
}

// siteState tracks the per-URL scheduling cursor inside the poller.
// Mirrors the fields rippled's `setTimer` consults to pick the next
// site to fetch (ValidatorSite.cpp:213-228).
type siteState struct {
	occurrence  int
	uri         string
	targetURI   string
	interval    time.Duration
	nextRefresh time.Time
}

type redirectTrace struct {
	permanentTarget     string
	permanentRedirected bool
	responseURI         string
	onPermanent         func(string)
}

type redirectTraceKey struct{}

type redirectError struct {
	message string
}

func (e *redirectError) Error() string {
	return e.message
}

type fetchFailureClass uint8

const (
	fetchFailureTransport fetchFailureClass = iota
	fetchFailureStatus
	fetchFailureRedirect
)

type fetchFailure struct {
	class  fetchFailureClass
	err    error
	detail error
}

func (e *fetchFailure) Error() string {
	if e.detail != nil {
		return e.detail.Error()
	}
	return e.err.Error()
}

func (e *fetchFailure) Unwrap() error {
	return e.err
}

type fetchResult struct {
	body                []byte
	responseURI         string
	targetURI           string
	permanentRedirected bool
	err                 error
}

// SitePoller fetches publisher list URLs on a periodic cadence and
// feeds the parsed envelopes into an Aggregator. Mirrors rippled's
// ValidatorSite. Fetches are serialized through a single timer-driven
// goroutine: rippled's `fetching_` flag guarantees at most one
// in-flight request across all sites (ValidatorSite.cpp:236, 625-629),
// and the BroadcastLatest hand-off on the success path is correct only
// under that same exclusion.
type SitePoller struct {
	aggregator *Aggregator
	client     *http.Client
	logger     *slog.Logger
	interval   time.Duration

	mu         sync.Mutex
	sites      []*siteState
	generation *pollGeneration
}

type pollGeneration struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSitePoller constructs a poller for the given URLs. Each URL is
// validated up front, mirroring rippled's `ValidatorSite::load()` which
// rejects unparseable / invalid URIs at startup
// (ValidatorSite.cpp:147-159 + Resource constructor at
// ValidatorSite.cpp:38-75). Returns an error on the first invalid URI
// so misconfiguration surfaces immediately rather than only after the
// first periodic fetch fails. Passing a nil aggregator panics: the
// poller has nowhere to deliver what it fetches.
func NewSitePoller(uris []string, agg *Aggregator, logger *slog.Logger) (*SitePoller, error) {
	if agg == nil {
		panic("validator/list: SitePoller requires non-nil Aggregator")
	}
	if logger == nil {
		logger = slog.Default().With("component", "validator-list-site-poller")
	}
	sites := make([]*siteState, 0, len(uris))
	occurrences := make(map[string]int, len(uris))
	for _, u := range uris {
		if err := validateSiteURI(u); err != nil {
			return nil, fmt.Errorf("invalid validator site uri %q: %w", u, err)
		}
		sites = append(sites, &siteState{
			occurrence: occurrences[u],
			uri:        u,
			targetURI:  u,
			interval:   defaultRefreshInterval,
		})
		occurrences[u]++
	}
	p := &SitePoller{
		aggregator: agg,
		logger:     logger,
		interval:   defaultRefreshInterval,
		sites:      sites,
	}
	p.client = &http.Client{
		Timeout:       defaultRequestTimeout,
		CheckRedirect: checkRedirect,
	}
	return p, nil
}

// validateSiteURI mirrors rippled's
// `ValidatorSite::Site::Resource::Resource(std::string uri)` at
// rippled/src/xrpld/app/misc/detail/ValidatorSite.cpp:38-75. Rejects:
//   - unparseable URIs
//   - file:// with a non-empty host (`file://ripple.com/...`)
//   - file:// with an empty path
//   - http:// / https:// without a hostname
//   - any scheme other than file, http, https
func validateSiteURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("cannot be parsed: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file":
		if parsed.Host != "" {
			return errors.New("file URI cannot contain a hostname")
		}
		if parsed.Path == "" {
			return errors.New("file URI must contain a path")
		}
	case "http", "https":
		if parsed.Host == "" {
			return fmt.Errorf("%s URI must contain a hostname", parsed.Scheme)
		}
	case "":
		return errors.New("missing scheme")
	default:
		return fmt.Errorf("unsupported scheme: %q", parsed.Scheme)
	}
	return nil
}

// checkRedirect is the redirect policy applied to every HTTP fetch.
// Caps the chain at maxRedirects and rejects any redirect target whose
// scheme is not http/https — mirrors rippled's processRedirect at
// rippled/src/xrpld/app/misc/detail/ValidatorSite.cpp:511-531. Without
// the scheme gate an attacker controlling the publisher hostname (or
// any intermediate redirect target) could send the fetcher at a
// `file:///etc/passwd` URL and read arbitrary local files.
func checkRedirect(req *http.Request, via []*http.Request) error {
	status := 0
	if req.Response != nil {
		status = req.Response.StatusCode
	}
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		// These are the only redirect statuses rippled follows.
	default:
		return http.ErrUseLastResponse
	}
	location := ""
	if req.Response != nil {
		location = req.Response.Header.Get("Location")
	}
	if location == "" {
		return &redirectError{message: "missing location"}
	}
	if len(via) > maxRedirects {
		return &redirectError{message: "max redirects"}
	}
	if err := validateRedirectLocation(location); err != nil {
		return err
	}
	if trace, ok := req.Context().Value(redirectTraceKey{}).(*redirectTrace); ok {
		trace.responseURI = location
		if status == http.StatusMovedPermanently || status == http.StatusPermanentRedirect {
			trace.permanentTarget = location
			trace.permanentRedirected = true
			if trace.onPermanent != nil {
				trace.onPermanent(trace.permanentTarget)
			}
		}
	}
	return nil
}

func validateRedirectLocation(location string) *redirectError {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme == "" {
		return &redirectError{message: "URI '" + location + "' cannot be parsed"}
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "file":
		if parsed.Host != "" {
			return &redirectError{message: "file URI cannot contain a hostname"}
		}
		if parsed.Path == "" {
			return &redirectError{message: "file URI must contain a path"}
		}
		return &redirectError{message: "invalid scheme in redirect " + scheme}
	case "http", "https":
		if parsed.Host == "" {
			return &redirectError{message: "URI '" + location + "' cannot be parsed"}
		}
		return nil
	default:
		return &redirectError{message: "Unsupported scheme: '" + scheme + "'"}
	}
}

// SetInterval overrides the default poll interval. Useful for tests
// that don't want to wait 5 minutes between fetches; production
// callers rarely override. MUST be called before Start.
func (p *SitePoller) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.generation != nil {
		return
	}
	p.interval = d
	for _, s := range p.sites {
		s.interval = d
	}
}

// SetHTTPClient overrides the HTTP client. The CheckRedirect / Timeout
// fields are forced to the poller defaults so caller-supplied clients
// cannot inadvertently widen the redirect/scheme attack surface.
// MUST be called before Start.
func (p *SitePoller) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.generation != nil {
		return
	}
	clone := *c
	clone.CheckRedirect = checkRedirect
	if clone.Timeout <= 0 || clone.Timeout > defaultRequestTimeout {
		clone.Timeout = defaultRequestTimeout
	}
	p.client = &clone
}

// Start launches the polling goroutine. The goroutine drives all
// configured sites serially through a single timer, mirroring rippled's
// `setTimer` / `fetching_` exclusion at
// rippled/src/xrpld/app/misc/detail/ValidatorSite.cpp:208-228. The
// first iteration fires immediately (nextRefresh defaults to the zero
// time) so a fresh boot has up-to-date publisher state without waiting
// one full interval — matches rippled's constructor at
// ValidatorSite.cpp:82 (`nextRefresh{clock_type::now()}`).
//
// A second Start while the poller is already running is a no-op — a rogue
// second runLoop would double every poll and the post-accept BroadcastLatest
// fan-out the single-goroutine design exists to serialize. After a Stop the
// poller can be started again, mirroring rippled's ValidatorSite::start which
// rearms after stop (ValidatorSite.cpp:171-172, :198); each run owns a fresh
// stop channel created here.
func (p *SitePoller) Start(ctx context.Context) {
	p.mu.Lock()
	if len(p.sites) == 0 || p.generation != nil {
		p.mu.Unlock()
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	generation := &pollGeneration{cancel: cancel, done: make(chan struct{})}
	p.generation = generation
	for _, site := range p.sites {
		site.nextRefresh = time.Time{}
	}
	p.mu.Unlock()
	go func() {
		defer cancel()
		p.runLoop(runCtx, generation)
	}()
}

// Stop signals the polling goroutine to exit and blocks until it has.
// Safe to call multiple times; idempotent. A subsequent Start resumes
// polling on a fresh stop channel.
func (p *SitePoller) Stop() {
	p.mu.Lock()
	generation := p.generation
	if generation == nil {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	generation.cancel()
	<-generation.done
}

// runLoop drives the timer pattern: pick the site with the smallest
// nextRefresh, sleep until then, fetch it, update its nextRefresh,
// repeat. The single-goroutine design matches rippled's setTimer and
// avoids parallel BroadcastLatest races when two sites simultaneously
// deliver the same publisher's list.
func (p *SitePoller) runLoop(ctx context.Context, generation *pollGeneration) {
	defer func() {
		p.mu.Lock()
		if p.generation == generation {
			p.generation = nil
			close(generation.done)
		}
		p.mu.Unlock()
	}()

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		next := p.pickNext()
		now := time.Now().UTC()
		wait := time.Duration(0)
		if next != nil && next.nextRefresh.After(now) {
			wait = next.nextRefresh.Sub(now)
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if next == nil {
			continue
		}
		p.fetchSite(ctx, next)
	}
}

// pickNext returns the site with the smallest nextRefresh time. nil
// when no sites are configured.
func (p *SitePoller) pickNext() *siteState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sites) == 0 {
		return nil
	}
	best := p.sites[0]
	for _, s := range p.sites[1:] {
		if s.nextRefresh.Before(best.nextRefresh) {
			best = s
		}
	}
	return best
}

// fetchSite performs one fetch+apply cycle for a single site. Updates
// the site's internal interval/nextRefresh cursor and pushes the
// outcome through to the aggregator for RPC visibility.
func (p *SitePoller) fetchSite(ctx context.Context, s *siteState) {
	p.mu.Lock()
	uri := s.targetURI
	interval := s.interval
	p.mu.Unlock()
	if uri == "" {
		uri = s.uri
	}
	// Surface the upcoming refresh time before the (possibly slow) fetch
	// runs, mirroring rippled's onTimer ordering at ValidatorSite.cpp:354-355
	// where nextRefresh is set before makeRequest. Without this the
	// validator_list_sites RPC reports a stale, already-past
	// next_refresh_time for the duration of an in-flight fetch.
	p.aggregator.setNextRefreshOccurrence(s.uri, s.occurrence, time.Now().UTC().Add(interval))

	result := p.fetchWithRedirect(ctx, uri, func(target string) {
		p.mu.Lock()
		s.targetURI = target
		p.mu.Unlock()
		p.aggregator.setSiteTargetOccurrence(s.uri, s.occurrence, target)
	})
	if result.err != nil {
		lastErr, retry := fetchDiagnostic(result.err)
		p.recordFailure(s, retry, lastErr, "validator list site fetch failed", "uri", uri, "error", result.err)
		return
	}
	responseURI := result.responseURI
	if responseURI == "" {
		responseURI = uri
	}

	var env envelope
	if jsonErr := json.Unmarshal(result.body, &env); jsonErr != nil {
		p.recordFailure(s, false, "bad json", "validator list site JSON decode failed", "uri", responseURI, "error", jsonErr)
		return
	}

	// Rippled requires `manifest` (string) and `version` (int) at the
	// envelope level for both v1 and v2 — see ValidatorList::parseBlobs
	// and ValidatorSite.cpp:391-410. The literal "Missing fields in
	// JSON response" message is the rippled-faithful error text.
	if err := env.validateShape(); err != nil {
		p.recordFailure(s, false, missingFieldsStatusMessage, missingFieldsMessage, "uri", responseURI)
		return
	}

	// Dispatch strictly on the envelope's declared version — rippled's
	// parseBlobs switches on `version`, NOT on which fields happen to
	// be populated. A v2 envelope that only carries top-level
	// blob/signature is invalid (no blobs_v2), regardless of whether
	// v1 fields are present.
	var disp Disposition
	var dispList []Disposition
	var pubKey PublisherKey
	switch {
	case env.Version.Value != 1:
		dispList, pubKey, _ = p.aggregator.applyCollectionFromSite(env.toCollection(), responseURI)
		disp = bestDisposition(dispList)
	case env.Version.Value == 1:
		disp, pubKey, _ = p.aggregator.applyListInternal(
			[]byte(env.Manifest.Value),
			nil,
			false,
			[]byte(env.Blob.Value),
			[]byte(env.Signature.Value),
			env.Version.Value,
			responseURI,
			true,
		)
	}

	// Capture time AFTER apply completes — mirrors rippled
	// ValidatorSite.cpp:430 which writes `Site::Status{clock_type::now(), ...}`
	// at the apply boundary, not pre-fetch. The RPC `last_refresh_time`
	// reflects when the list was applied, not when the fetch began.
	applyTime := time.Now().UTC()

	// Push the canonical accepted form out to peers. Mirrors rippled
	// applyListsAndBroadcast at ValidatorSite.cpp:418-427.
	if disp.ShouldRelay() && pubKey != (PublisherKey{}) {
		p.aggregator.BroadcastLatest(pubKey, 0)
	}

	// Per-publisher refresh override (clamped). Rippled parses with
	// `asUInt()` so fractional minutes are truncated
	// (ValidatorSite.cpp:484-489).
	chosenInterval := p.interval
	refreshSec := 0
	if refreshMin := int(env.RefreshMinutes.Value); env.RefreshMinutes.Valid && refreshMin > 0 {
		d := time.Duration(refreshMin) * time.Minute
		if d < defaultMinRefresh {
			d = defaultMinRefresh
		} else if d > defaultMaxRefresh {
			d = defaultMaxRefresh
		}
		chosenInterval = d
		refreshSec = int(d / time.Second)
	}

	// A successfully parsed response has an empty status message even
	// when semantic validation rejects the list.
	lastSuccess := time.Time{}
	if disp.ShouldRelay() {
		lastSuccess = applyTime
	}
	nextAt := applyTime.Add(chosenInterval)
	p.aggregator.updateSiteStateOccurrence(s.uri, s.occurrence, applyTime, lastSuccess, "", disp, refreshSec, nextAt)

	p.mu.Lock()
	s.interval = chosenInterval
	s.nextRefresh = nextAt
	p.mu.Unlock()
}

// recordFailure pushes a fetch / parse failure through to the aggregator
// and the per-site scheduling cursor. The normalized message becomes the
// `last_refresh_message` surfaced via RPC; detailed errors remain in logs.
//
// retry distinguishes a transient transport failure from a permanent
// parse/validation failure, mirroring rippled's `retry` flag in
// onSiteFetch (ValidatorSite.cpp:551-620). A transport or HTTP-status
// error reschedules at the short error_retry_interval (30s, :558-560);
// a malformed-but-fetched payload waits the full refresh interval —
// rippled leaves nextRefresh at the value set before the request
// (:354-355) and the parse exception does not shorten it (:615-620) — so
// a persistently broken site is not re-polled every 30s.
func (p *SitePoller) recordFailure(s *siteState, retry bool, lastErr, logMsg string, logFields ...any) {
	now := time.Now().UTC()
	p.logger.Warn(logMsg, logFields...)
	p.mu.Lock()
	nextAt := now.Add(s.interval)
	if retry {
		nextAt = now.Add(errorRetryInterval)
	}
	s.nextRefresh = nextAt
	p.mu.Unlock()
	p.aggregator.updateSiteStateOccurrence(s.uri, s.occurrence, now, time.Time{}, lastErr, Malformed, 0, nextAt)
}

// fetch retrieves the raw envelope body from the given URI. Scheme
// validation has already happened at construction time, so the switch
// here is structural rather than defensive.
func (p *SitePoller) fetch(ctx context.Context, uri string) ([]byte, error) {
	result := p.fetchWithRedirect(ctx, uri, nil)
	return result.body, result.err
}

func (p *SitePoller) fetchWithRedirect(ctx context.Context, uri string, onPermanent func(string)) fetchResult {
	if ctx == nil {
		ctx = context.Background()
	}
	trace := &redirectTrace{permanentTarget: uri, responseURI: uri, onPermanent: onPermanent}
	ctx = context.WithValue(ctx, redirectTraceKey{}, trace)
	result := fetchResult{targetURI: uri}
	parsed, err := url.Parse(uri)
	if err != nil {
		result.err = &fetchFailure{class: fetchFailureTransport, err: fmt.Errorf("parse URI: %w", err)}
		return result
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			result.err = &fetchFailure{class: fetchFailureTransport, err: fmt.Errorf("build request: %w", err)}
			return result
		}
		resp, err := p.client.Do(req)
		if err != nil {
			failureClass := fetchFailureTransport
			var redirectErr *redirectError
			if errors.As(err, &redirectErr) {
				failureClass = fetchFailureRedirect
			}
			failure := fmt.Errorf("HTTP GET: %w", err)
			if failureClass == fetchFailureRedirect {
				result.targetURI = trace.permanentTarget
				result.permanentRedirected = trace.permanentRedirected
				result.err = &fetchFailure{
					class:  failureClass,
					err:    redirectErr,
					detail: failure,
				}
				return result
			}
			result.targetURI = trace.permanentTarget
			result.permanentRedirected = trace.permanentRedirected
			result.err = &fetchFailure{
				class: failureClass,
				err:   failure,
			}
			return result
		}
		defer resp.Body.Close()
		if isRedirectStatus(resp.StatusCode) && resp.Header.Get("Location") == "" {
			result.targetURI = trace.permanentTarget
			result.permanentRedirected = trace.permanentRedirected
			result.err = &fetchFailure{
				class: fetchFailureRedirect,
				err:   errors.New("missing location"),
			}
			return result
		}
		if resp.StatusCode != http.StatusOK {
			result.targetURI = trace.permanentTarget
			result.permanentRedirected = trace.permanentRedirected
			result.err = &fetchFailure{
				class: fetchFailureStatus,
				err:   fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status),
			}
			return result
		}
		body, err := readBoundedBody(resp.Body)
		if err != nil {
			result.targetURI = trace.permanentTarget
			result.permanentRedirected = trace.permanentRedirected
			result.err = &fetchFailure{
				class: fetchFailureTransport,
				err:   err,
			}
			return result
		}
		result.responseURI = trace.responseURI
		result.body = body
		result.targetURI = trace.permanentTarget
		result.permanentRedirected = trace.permanentRedirected
		return result
	case "file":
		// Host already rejected at construction (see validateSiteURI);
		// Path already guaranteed non-empty.
		body, err := readBoundedFile(parsed.Path)
		if err != nil {
			result.err = &fetchFailure{class: fetchFailureTransport, err: err}
			return result
		}
		result.body = body
		result.responseURI = uri
		return result
	default:
		result.err = &fetchFailure{
			class: fetchFailureTransport,
			err:   fmt.Errorf("unsupported URI scheme %q", parsed.Scheme),
		}
		return result
	}
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func fetchDiagnostic(err error) (string, bool) {
	var failure *fetchFailure
	if !errors.As(err, &failure) {
		return "fetch error", true
	}
	switch failure.class {
	case fetchFailureStatus:
		return "bad result code", true
	case fetchFailureRedirect:
		return failure.err.Error(), false
	default:
		return "fetch error", true
	}
}

// bestDisposition reduces a collection of dispositions to the single
// summary the caller (RPC, logs) sees, using Disposition.Severity as
// the canonical "lower-is-better" ordering. Mirrors rippled's
// PublisherListStats best-of-many reduction.
func bestDisposition(dispList []Disposition) Disposition {
	if len(dispList) == 0 {
		return Malformed
	}
	best := dispList[0]
	for _, d := range dispList[1:] {
		if d.Severity() < best.Severity() {
			best = d
		}
	}
	return best
}

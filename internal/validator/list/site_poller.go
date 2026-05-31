package list

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// DefaultRefreshInterval matches rippled's
// ValidatorSite::default_refresh_interval — 5 minutes between polls of
// a configured publisher URL.
const DefaultRefreshInterval = 5 * time.Minute

// DefaultMinRefresh is the floor applied to a publisher-supplied
// refresh interval. One minute matches rippled's clamp at
// ValidatorSite.cpp:486.
const DefaultMinRefresh = 1 * time.Minute

// DefaultMaxRefresh is the ceiling applied to a publisher-supplied
// refresh interval. 24 hours matches rippled.
const DefaultMaxRefresh = 24 * time.Hour

// DefaultRequestTimeout caps a single HTTP fetch attempt. Mirrors
// rippled's ValidatorSite constructor default at
// rippled/src/xrpld/app/misc/ValidatorSite.h:142-145
// (`std::chrono::seconds timeout = std::chrono::seconds{20}`).
const DefaultRequestTimeout = 20 * time.Second

// MaxRedirects caps the number of HTTP redirects followed during a
// single fetch. Matches rippled ValidatorSite.cpp:36 `max_redirects = 3`.
const MaxRedirects = 3

// ErrorRetryInterval is the cadence used to retry a failed fetch.
// Mirrors rippled's error_retry_interval at ValidatorSite.cpp:35.
const ErrorRetryInterval = 30 * time.Second

// missingFieldsMessage mirrors rippled's exception text from
// ValidatorSite.cpp::parseJsonResponse ("Missing fields in JSON
// response") so external monitors keyed on the literal string match.
const missingFieldsMessage = "Missing fields in JSON response"

// envelopeJSON is the JSON shape published at vl.* URLs (and the
// equivalent file:// payloads).
type envelopeJSON struct {
	Manifest       string             `json:"manifest"`
	Blob           string             `json:"blob,omitempty"`
	Signature      string             `json:"signature,omitempty"`
	Version        uint32             `json:"version"`
	PublicKey      string             `json:"public_key,omitempty"`
	BlobsV2        []envelopeBlobJSON `json:"blobs_v2,omitempty"`
	RefreshMinutes float64            `json:"refresh_interval,omitempty"`
}

// envelopeBlobJSON is a v2 collection entry inside the JSON envelope.
type envelopeBlobJSON struct {
	Manifest  string `json:"manifest,omitempty"`
	Blob      string `json:"blob"`
	Signature string `json:"signature"`
}

// siteState tracks the per-URL scheduling cursor inside the poller.
// Mirrors the fields rippled's `setTimer` consults to pick the next
// site to fetch (ValidatorSite.cpp:213-228).
type siteState struct {
	uri         string
	interval    time.Duration
	nextRefresh time.Time
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

	mu    sync.Mutex
	sites []*siteState
	wg    sync.WaitGroup
	stop  chan struct{}
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
	for _, u := range uris {
		if err := validateSiteURI(u); err != nil {
			return nil, fmt.Errorf("invalid validator site uri %q: %w", u, err)
		}
		sites = append(sites, &siteState{uri: u, interval: DefaultRefreshInterval})
	}
	p := &SitePoller{
		aggregator: agg,
		logger:     logger,
		interval:   DefaultRefreshInterval,
		sites:      sites,
		stop:       make(chan struct{}),
	}
	p.client = &http.Client{
		Timeout:       DefaultRequestTimeout,
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
// Caps the chain at MaxRedirects and rejects any redirect target whose
// scheme is not http/https — mirrors rippled's processRedirect at
// rippled/src/xrpld/app/misc/detail/ValidatorSite.cpp:511-531. Without
// the scheme gate an attacker controlling the publisher hostname (or
// any intermediate redirect target) could send the fetcher at a
// `file:///etc/passwd` URL and read arbitrary local files.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("max redirects (%d) exceeded", MaxRedirects)
	}
	scheme := strings.ToLower(req.URL.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("invalid scheme in redirect %q", scheme)
	}
	return nil
}

// SetInterval overrides the default poll interval. Useful for tests
// that don't want to wait 5 minutes between fetches; production
// callers rarely override. MUST be called before Start.
func (p *SitePoller) SetInterval(d time.Duration) {
	if d <= 0 {
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
	c.CheckRedirect = checkRedirect
	if c.Timeout == 0 {
		c.Timeout = DefaultRequestTimeout
	}
	p.client = c
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
// Safe to call once; subsequent calls are no-ops.
func (p *SitePoller) Start(ctx context.Context) {
	if len(p.sites) == 0 {
		return
	}
	p.wg.Add(1)
	go p.runLoop(ctx)
}

// Stop signals the polling goroutine to exit and blocks until it has.
// Safe to call multiple times; idempotent.
func (p *SitePoller) Stop() {
	select {
	case <-p.stop:
		return
	default:
		close(p.stop)
	}
	p.wg.Wait()
}

// runLoop drives the timer pattern: pick the site with the smallest
// nextRefresh, sleep until then, fetch it, update its nextRefresh,
// repeat. The single-goroutine design matches rippled's setTimer and
// avoids parallel BroadcastLatest races when two sites simultaneously
// deliver the same publisher's list.
func (p *SitePoller) runLoop(ctx context.Context) {
	defer p.wg.Done()

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
		case <-p.stop:
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
	uri := s.uri
	body, err := p.fetch(ctx, uri)
	if err != nil {
		p.recordFailure(s, err.Error(), "validator list site fetch failed", "error", err)
		return
	}

	var env envelopeJSON
	if jsonErr := json.Unmarshal(body, &env); jsonErr != nil {
		msg := fmt.Sprintf("envelope JSON decode: %v", jsonErr)
		p.recordFailure(s, msg, msg, "uri", uri)
		return
	}

	// Rippled requires `manifest` (string) and `version` (int) at the
	// envelope level for both v1 and v2 — see ValidatorList::parseBlobs
	// and ValidatorSite.cpp:391-410. The literal "Missing fields in
	// JSON response" message is the rippled-faithful error text.
	if env.Version == 0 || env.Manifest == "" {
		p.recordFailure(s, missingFieldsMessage, missingFieldsMessage, "uri", uri)
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
	case env.Version >= 2:
		if len(env.BlobsV2) == 0 {
			p.recordFailure(s, missingFieldsMessage, missingFieldsMessage, "uri", uri, "version", env.Version)
			return
		}
		coll := &message.ValidatorListCollection{
			Version:  env.Version,
			Manifest: []byte(env.Manifest),
		}
		for _, b := range env.BlobsV2 {
			coll.Blobs = append(coll.Blobs, message.ValidatorBlobInfo{
				Manifest:  []byte(b.Manifest),
				Blob:      []byte(b.Blob),
				Signature: []byte(b.Signature),
			})
		}
		dispList, pubKey, _ = p.aggregator.ApplyCollection(coll, uri)
		disp = bestDisposition(dispList)
	case env.Version == 1:
		if env.Blob == "" || env.Signature == "" {
			p.recordFailure(s, missingFieldsMessage, missingFieldsMessage, "uri", uri)
			return
		}
		disp, pubKey, _ = p.aggregator.ApplyList(
			[]byte(env.Manifest),
			[]byte(env.Blob),
			[]byte(env.Signature),
			env.Version,
			uri,
		)
	default:
		p.recordFailure(s, missingFieldsMessage, missingFieldsMessage, "uri", uri, "version", env.Version)
		return
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
	if refreshMin := int(env.RefreshMinutes); refreshMin > 0 {
		d := time.Duration(refreshMin) * time.Minute
		if d < DefaultMinRefresh {
			d = DefaultMinRefresh
		} else if d > DefaultMaxRefresh {
			d = DefaultMaxRefresh
		}
		chosenInterval = d
		refreshSec = int(d / time.Second)
	}

	// rippled emits an empty `last_refresh_message` whenever the parse
	// succeeded — the disposition itself carries the outcome
	// (ValidatorSite.cpp:430). Stash the disposition string in the
	// message only for dispositions that indicate the apply rejected
	// the list (anything not ShouldRelay-eligible).
	lastSuccess := time.Time{}
	lastErr := ""
	if disp.ShouldRelay() {
		lastSuccess = applyTime
	} else {
		lastErr = "disposition=" + disp.String()
	}
	nextAt := applyTime.Add(chosenInterval)
	p.aggregator.UpdateSiteState(uri, applyTime, lastSuccess, lastErr, disp, refreshSec, nextAt)

	p.mu.Lock()
	s.interval = chosenInterval
	s.nextRefresh = nextAt
	p.mu.Unlock()
}

// recordFailure pushes a fetch / parse failure through to the
// aggregator and the per-site scheduling cursor. The error message
// becomes both the log line and the `last_refresh_message` surfaced
// via RPC. NextRefresh is set to now+ErrorRetryInterval to mirror
// rippled's error_retry_interval at ValidatorSite.cpp:555-561.
func (p *SitePoller) recordFailure(s *siteState, lastErr, logMsg string, logFields ...any) {
	now := time.Now().UTC()
	nextAt := now.Add(ErrorRetryInterval)
	p.logger.Warn(logMsg, logFields...)
	p.aggregator.UpdateSiteState(s.uri, now, time.Time{}, lastErr, Malformed, 0, nextAt)
	p.mu.Lock()
	s.nextRefresh = nextAt
	p.mu.Unlock()
}

// fetch retrieves the raw envelope body from the given URI. Scheme
// validation has already happened at construction time, so the switch
// here is structural rather than defensive.
func (p *SitePoller) fetch(ctx context.Context, uri string) ([]byte, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse URI: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP GET: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
		}
		const maxBody = 8 << 20
		return io.ReadAll(io.LimitReader(resp.Body, maxBody))
	case "file":
		// Host already rejected at construction (see validateSiteURI);
		// Path already guaranteed non-empty.
		return os.ReadFile(parsed.Path)
	default:
		return nil, fmt.Errorf("unsupported URI scheme %q", parsed.Scheme)
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

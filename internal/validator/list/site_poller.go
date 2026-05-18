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

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// DefaultRefreshInterval matches rippled's
// ValidatorSite::default_refresh_interval — 5 minutes between polls of
// a configured publisher URL. Operators rarely override; the publisher
// JSON envelope may carry a per-site refresh override which the poller
// honors within [DefaultMinRefresh, DefaultMaxRefresh].
const DefaultRefreshInterval = 5 * time.Minute

// DefaultMinRefresh is the floor applied to a publisher-supplied
// refresh interval. One minute matches rippled's clamp at
// ValidatorSite.cpp:486.
const DefaultMinRefresh = 1 * time.Minute

// DefaultMaxRefresh is the ceiling applied to a publisher-supplied
// refresh interval. 24 hours matches rippled.
const DefaultMaxRefresh = 24 * time.Hour

// DefaultRequestTimeout caps a single HTTP fetch attempt. Rippled uses
// 30s in ValidatorSite::activeFetcher; we mirror.
const DefaultRequestTimeout = 30 * time.Second

// MaxRedirects caps the number of HTTP redirects followed during a
// single fetch. Matches rippled ValidatorSite.cpp:36 `max_redirects = 3`.
const MaxRedirects = 3

// ErrorRetryInterval is the cadence used to retry a failed fetch
// (network error, non-2xx status, JSON decode failure). Mirrors
// rippled's error_retry_interval at ValidatorSite.cpp:35 and the
// onError(...,retry=true) branch at ValidatorSite.cpp:555-561.
const ErrorRetryInterval = 30 * time.Second

// envelopeJSON is the JSON shape published at vl.* URLs (and the
// equivalent file:// payloads). Decoded from the HTTP response body.
//
// RefreshMinutes mirrors rippled's `refresh_interval` field which is
// parsed as `std::chrono::minutes{body[jss::refresh_interval].asUInt()}`
// (ValidatorSite.cpp:484-489). Keep the wire-side unit explicit in the
// field name so future readers do not redo this conformance check.
type envelopeJSON struct {
	Manifest       string             `json:"manifest"`
	Blob           string             `json:"blob,omitempty"`
	Signature      string             `json:"signature,omitempty"`
	Version        uint32             `json:"version"`
	PublicKey      string             `json:"public_key,omitempty"`
	BlobsV2        []envelopeBlobJSON `json:"blobs_v2,omitempty"`
	RefreshMinutes int                `json:"refresh_interval,omitempty"`
}

// envelopeBlobJSON is a v2 collection entry inside the JSON envelope.
type envelopeBlobJSON struct {
	Manifest  string `json:"manifest,omitempty"`
	Blob      string `json:"blob"`
	Signature string `json:"signature"`
}

// SitePoller fetches publisher list URLs on a periodic cadence and
// feeds the parsed envelopes into an Aggregator. Mirrors rippled's
// ValidatorSite at rippled/src/xrpld/app/misc/ValidatorSite.cpp.
//
// One goroutine per configured URL; per-URL state (next refresh time,
// last error) lives on the Aggregator so the validator_list_sites RPC
// can read it without traversing the poller's internals.
type SitePoller struct {
	uris       []string
	aggregator *Aggregator
	client     *http.Client
	logger     *slog.Logger
	interval   time.Duration

	wg   sync.WaitGroup
	stop chan struct{}
}

// NewSitePoller constructs a poller for the given URLs. Passing zero
// URLs yields an inert poller — Run / Stop are still safe to call.
// Passing a nil aggregator panics: the poller has nowhere to deliver
// what it fetches.
func NewSitePoller(uris []string, agg *Aggregator, logger *slog.Logger) *SitePoller {
	if agg == nil {
		panic("validator/list: SitePoller requires non-nil Aggregator")
	}
	if logger == nil {
		logger = slog.Default().With("component", "validator-list-site-poller")
	}
	return &SitePoller{
		uris:       uris,
		aggregator: agg,
		client: &http.Client{
			Timeout: DefaultRequestTimeout,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= MaxRedirects {
					return fmt.Errorf("stopped after %d redirects", MaxRedirects)
				}
				return nil
			},
		},
		logger:   logger,
		interval: DefaultRefreshInterval,
		stop:     make(chan struct{}),
	}
}

// SetInterval overrides the default poll interval. Useful for tests that
// don't want to wait 5 minutes between fetches; production callers
// rarely override.
//
// MUST be called before Start — there is no synchronization between
// runOne goroutines and these setters.
func (p *SitePoller) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	p.interval = d
}

// SetHTTPClient overrides the HTTP client. Useful for tests that wire
// a custom Transport (e.g. recording requests in-memory).
//
// MUST be called before Start — there is no synchronization between
// runOne goroutines and these setters.
func (p *SitePoller) SetHTTPClient(c *http.Client) {
	if c != nil {
		p.client = c
	}
}

// Start launches one goroutine per configured URL. Each goroutine
// performs an immediate fetch (so the initial trust set is populated
// without waiting one refresh period) then loops on the configured
// interval. Safe to call once; subsequent calls are no-ops while the
// poller is running.
func (p *SitePoller) Start(ctx context.Context) {
	if len(p.uris) == 0 {
		return
	}
	for _, u := range p.uris {
		p.wg.Add(1)
		go p.runOne(ctx, u)
	}
}

// Stop signals all polling goroutines to exit and blocks until they
// have. Safe to call multiple times; idempotent.
func (p *SitePoller) Stop() {
	select {
	case <-p.stop:
		return
	default:
		close(p.stop)
	}
	p.wg.Wait()
}

func (p *SitePoller) runOne(ctx context.Context, uri string) {
	defer p.wg.Done()

	interval := p.interval
	for {
		// Immediate fetch on entry, then sleep — so a fresh boot has
		// up-to-date publisher state without waiting one full
		// interval.
		nextInterval := p.fetchAndApply(ctx, uri)
		if nextInterval > 0 {
			interval = nextInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-time.After(interval):
		}
	}
}

// fetchAndApply runs a single fetch attempt, parses the envelope, and
// feeds it into the aggregator. Updates the per-site state on the
// aggregator (last-fetched / last-error / disposition) regardless of
// outcome so the validator_list_sites RPC reflects every attempt.
//
// Returns the next refresh interval to use:
//   - On fetch / decode failure: ErrorRetryInterval (30s), mirroring
//     rippled's error_retry_interval.
//   - On success with an envelope refresh_interval: the clamped value.
//   - Otherwise: zero, meaning "keep using the caller's interval".
func (p *SitePoller) fetchAndApply(ctx context.Context, uri string) time.Duration {
	now := time.Now().UTC()
	// Schedule the next refresh time BEFORE the fetch begins, mirroring
	// rippled's onTimer ordering (ValidatorSite.cpp:354-355) so the RPC
	// reports the upcoming wakeup even while this fetch is outstanding.
	// Errors and per-publisher overrides reset NextRefresh in the
	// UpdateSiteState call below.
	p.aggregator.SetNextRefresh(uri, now.Add(p.interval))

	body, err := p.fetch(ctx, uri)
	if err != nil {
		p.logger.Warn("validator list site fetch failed",
			"uri", uri, "error", err)
		p.aggregator.UpdateSiteState(uri, now, time.Time{}, err.Error(), Malformed, 0, now.Add(ErrorRetryInterval))
		return ErrorRetryInterval
	}

	var env envelopeJSON
	if err := json.Unmarshal(body, &env); err != nil {
		msg := fmt.Sprintf("envelope JSON decode: %v", err)
		p.logger.Warn(msg, "uri", uri)
		p.aggregator.UpdateSiteState(uri, now, time.Time{}, msg, Malformed, 0, now.Add(ErrorRetryInterval))
		return ErrorRetryInterval
	}

	// Rippled ValidatorSite.cpp:391-393 requires `version` to be a
	// present integer; a missing field fails validation and is logged
	// as "missing fields". Treat a zero/absent version as Malformed so
	// the RPC reports a real disposition rather than silently coercing
	// the envelope into v1.
	if env.Version == 0 {
		msg := "envelope missing required `version` field"
		p.logger.Warn(msg, "uri", uri)
		p.aggregator.UpdateSiteState(uri, now, time.Time{}, msg, Malformed, 0, now.Add(ErrorRetryInterval))
		return ErrorRetryInterval
	}

	var disp Disposition
	var dispList []Disposition
	if len(env.BlobsV2) > 0 {
		// v2 envelope: collection of forward-dated blobs.
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
		dispList, _ = p.aggregator.ApplyCollection(coll, uri)
		disp = bestDisposition(dispList)
	} else if env.Blob != "" && env.Signature != "" && env.Manifest != "" {
		// v1 envelope.
		disp, _ = p.aggregator.ApplyList(
			[]byte(env.Manifest),
			[]byte(env.Blob),
			[]byte(env.Signature),
			env.Version,
			uri,
		)
	} else {
		disp = Malformed
		p.logger.Warn("validator list envelope missing required fields", "uri", uri)
	}

	refreshSec := 0
	nextInterval := time.Duration(0)
	if env.RefreshMinutes > 0 {
		d := time.Duration(env.RefreshMinutes) * time.Minute
		if d < DefaultMinRefresh {
			d = DefaultMinRefresh
		} else if d > DefaultMaxRefresh {
			d = DefaultMaxRefresh
		}
		nextInterval = d
		refreshSec = int(d / time.Second)
	}

	lastSuccess := time.Time{}
	lastErr := ""
	if disp == Accepted || disp == SameSequence || disp == Pending || disp == Expired {
		lastSuccess = now
	} else {
		lastErr = "disposition=" + disp.String()
	}
	// nextInterval is 0 here when no per-publisher refresh override was
	// present in the envelope; in that case the caller will reuse its
	// configured interval. Compute the wall-clock next refresh for the
	// validator_list_sites RPC accordingly.
	scheduled := nextInterval
	if scheduled == 0 {
		scheduled = p.interval
	}
	p.aggregator.UpdateSiteState(uri, now, lastSuccess, lastErr, disp, refreshSec, now.Add(scheduled))

	return nextInterval
}

// fetch retrieves the raw envelope body from the given URI. Supports
// http://, https:// (via the package HTTP client) and file:// for
// operators wanting to point at a locally-mirrored list. Other
// schemes return an explicit error so misconfiguration surfaces
// immediately.
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
		req.Header.Set("User-Agent", "goXRPLd/validator-list-fetcher")
		req.Header.Set("Accept", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP GET: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
		}
		// Cap body size at 8 MiB — vl.ripple.com responses are ~30 KiB.
		const maxBody = 8 << 20
		return io.ReadAll(io.LimitReader(resp.Body, maxBody))
	case "file":
		path := parsed.Path
		if path == "" {
			return nil, errors.New("file URI missing path")
		}
		return os.ReadFile(path)
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

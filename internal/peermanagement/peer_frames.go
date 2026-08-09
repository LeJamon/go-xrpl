package peermanagement

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

type frameProgressReader struct {
	peer                *Peer
	reader              io.Reader
	conn                net.Conn
	frameID             uint64
	startedAt           time.Time
	deadline            time.Time
	budgetDeadlineArmed bool
	header              message.Header
	headerSet           bool
	bytesRead           uint64
	payloadStart        uint64
	payloadStartedAt    time.Time
}

func (p *Peer) newFrameProgressReader(reader io.Reader, conn net.Conn) *frameProgressReader {
	now := p.readPolicy.now()
	p.readProgressMu.Lock()
	p.readProgress.id++
	p.readProgress.active = true
	p.readProgress.deadline = time.Time{}
	p.readProgress.lastProgress = time.Time{}
	id := p.readProgress.id
	p.readProgressMu.Unlock()
	return &frameProgressReader{
		peer:      p,
		reader:    reader,
		conn:      conn,
		frameID:   id,
		startedAt: now,
	}
}

func (r *frameProgressReader) setHeader(header message.Header) error {
	r.header = header
	r.headerSet = true
	r.payloadStart = r.bytesRead
	r.payloadStartedAt = r.peer.readPolicy.now()
	r.deadline = r.startedAt.Add(r.peer.frameReadBudget(header.PayloadSize))

	r.peer.readProgressMu.Lock()
	if r.peer.readProgress.id == r.frameID && r.peer.readProgress.active {
		r.peer.readProgress.deadline = r.deadline
	}
	r.peer.readProgressMu.Unlock()
	return nil
}

func (p *Peer) frameReadBudget(payloadSize uint32) time.Duration {
	return p.readPolicy.idleTimeout + time.Duration(
		(int64(payloadSize)*int64(time.Second)+p.readPolicy.minimumFrameRate-1)/
			p.readPolicy.minimumFrameRate,
	)
}

func (r *frameProgressReader) Read(dst []byte) (int, error) {
	now := r.peer.readPolicy.now()
	if !r.deadline.IsZero() && !now.Before(r.deadline) {
		return 0, ErrFrameReadTooSlow
	}
	if r.conn != nil {
		deadline := now.Add(r.peer.readPolicy.idleTimeout)
		r.budgetDeadlineArmed = false
		if !r.deadline.IsZero() && !deadline.Before(r.deadline) {
			deadline = r.deadline
			r.budgetDeadlineArmed = true
		}
		if err := r.conn.SetReadDeadline(deadline); err != nil {
			return 0, err
		}
	}

	n, err := r.reader.Read(dst)
	if n > 0 {
		r.bytesRead += uint64(n)
		now = r.peer.readPolicy.now()
		r.peer.readProgressMu.Lock()
		if r.peer.readProgress.id == r.frameID && r.peer.readProgress.active {
			r.peer.readProgress.lastProgress = now
		}
		r.peer.readProgressMu.Unlock()
		if r.headerSet && r.peer.onBootstrapProgress != nil {
			r.peer.onBootstrapProgress(bootstrapFrameProgress{
				messageType: r.header.MessageType,
				wireSize:    r.header.PayloadSize,
				compressed:  r.header.Compressed,
				bytesRead:   r.bytesRead - r.payloadStart,
				elapsed:     now.Sub(r.payloadStartedAt),
			})
		}
		if !r.deadline.IsZero() && !now.Before(r.deadline) {
			return n, ErrFrameReadTooSlow
		}
	}
	return n, err
}

func (r *frameProgressReader) failure(err error, now time.Time) error {
	if !r.headerSet {
		return err
	}
	bytesRead := r.bytesRead - r.payloadStart
	elapsed := now.Sub(r.payloadStartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return &FrameReadError{
		MessageType: r.header.MessageType,
		WireSize:    r.header.PayloadSize,
		Compressed:  r.header.Compressed,
		BytesRead:   bytesRead,
		Elapsed:     elapsed,
		Err:         err,
	}
}

func (r *frameProgressReader) finish(success bool, now time.Time) {
	r.peer.readProgressMu.Lock()
	if r.peer.readProgress.id != r.frameID {
		r.peer.readProgressMu.Unlock()
		return
	}
	lastProgress := r.peer.readProgress.lastProgress
	r.peer.readProgress.active = false
	if success {
		r.peer.latencyMu.Lock()
		for seq, ping := range r.peer.pingsInFlight {
			if lastProgress.After(ping.sentAt) && now.Before(ping.progressDeadline) {
				ping.deferUntil = now.Add(r.peer.readPolicy.pingDispatchGrace)
				if ping.progressDeadline.Before(ping.deferUntil) {
					ping.deferUntil = ping.progressDeadline
				}
				r.peer.pingsInFlight[seq] = ping
			}
		}
		r.peer.latencyMu.Unlock()
	}
	r.peer.readProgressMu.Unlock()
}

func (p *Peer) frameProgressBlocksPing(progress inboundFrameProgress, ping pingInFlight, now time.Time) bool {
	return progress.active &&
		now.Before(ping.progressDeadline) &&
		!progress.deadline.IsZero() && now.Before(progress.deadline) &&
		progress.lastProgress.After(ping.sentAt) &&
		now.Sub(progress.lastProgress) < p.readPolicy.idleTimeout
}

func normalizeManifestSpoolReadError(err error, budgetDeadlineArmed bool) error {
	var localSpoolErr *manifestSpoolLocalError
	if errors.As(err, &localSpoolErr) {
		return err
	}
	if errors.Is(err, ErrFrameReadTooSlow) {
		return ErrFrameReadTooSlow
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if budgetDeadlineArmed {
			return ErrFrameReadTooSlow
		}
		return ErrReadIdle
	}
	return err
}

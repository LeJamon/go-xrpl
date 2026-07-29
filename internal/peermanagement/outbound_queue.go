package peermanagement

import (
	"bytes"
	"context"
	"sync"
)

type OutboundSendClass uint8

const (
	OutboundClassControl OutboundSendClass = iota
	OutboundClassConsensus
	OutboundClassAcquisition
	OutboundClassOrdinary
	OutboundClassBulk
	outboundClassCount
)

func (c OutboundSendClass) String() string {
	switch c {
	case OutboundClassControl:
		return "control"
	case OutboundClassConsensus:
		return "consensus"
	case OutboundClassAcquisition:
		return "acquisition"
	case OutboundClassOrdinary:
		return "ordinary"
	case OutboundClassBulk:
		return "bulk"
	default:
		return "unknown"
	}
}

type SendQueueFailureReason uint8

const (
	SendQueueClosed SendQueueFailureReason = iota
	SendQueueFrameLimit
	SendQueueByteLimit
	SendQueueSequenceLimit
	SendQueueSharedByteLimit
)

func (r SendQueueFailureReason) String() string {
	switch r {
	case SendQueueClosed:
		return "closed"
	case SendQueueFrameLimit:
		return "frame_limit"
	case SendQueueByteLimit:
		return "byte_limit"
	case SendQueueSequenceLimit:
		return "sequence_limit"
	case SendQueueSharedByteLimit:
		return "shared_byte_limit"
	default:
		return "unknown"
	}
}

type outboundFrame struct {
	data  []byte
	class OutboundSendClass
}

type outboundSequence struct {
	frames [][]byte
	next   int
}

type outboundToken struct {
	id    uint64
	data  []byte
	class OutboundSendClass
}

type outboundQueueSnapshot struct {
	ReliableQueued int
	BulkQueued     int
	InFlight       int
	TotalFrames    int

	ReliableBytes int64
	BulkBytes     int64
	InFlightBytes int64
	TotalBytes    int64

	BulkSequences int
	ClassFrames   [outboundClassCount]int
}

type outboundQueue struct {
	mu sync.Mutex

	reliable     []outboundFrame
	reliableHead int
	bulk         []outboundSequence
	bulkHead     int
	inFlight     *outboundToken
	nextTokenID  uint64

	classFrames [outboundClassCount]int
	totalFrames int
	totalBytes  int64

	reliableBytes int64
	bulkBytes     int64

	reliableBurst int
	closed        bool
	wake          chan struct{}
	fatal         chan error
	budget        *outboundBudgetAccount
	onFatal       func(*SendQueueError)
}

func newOutboundQueue() *outboundQueue {
	return &outboundQueue{
		reliable: make([]outboundFrame, 0, reliableSendBufferSize),
		bulk:     make([]outboundSequence, 0, bulkSequenceBufferSize),
		wake:     make(chan struct{}, 1),
		fatal:    make(chan error, 1),
	}
}

func (q *outboundQueue) setBudget(budget *outboundBudget) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.totalFrames != 0 || q.totalBytes != 0 {
		panic("cannot replace outbound budget with retained frames")
	}
	if q.budget != nil {
		q.budget.close()
	}
	q.budget = budget.attach()
}

func (q *outboundQueue) setFatalObserver(observer func(*SendQueueError)) {
	q.mu.Lock()
	q.onFatal = observer
	q.mu.Unlock()
}

func (q *outboundQueue) enqueueReliable(class OutboundSendClass, data []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return q.failureLocked(class, SendQueueClosed, 1, int64(len(data)))
	}
	if !q.canAdmitReliableLocked(class) {
		err := q.failureLocked(class, SendQueueFrameLimit, 1, int64(len(data)))
		q.signalFatalLocked(class, err)
		return err
	}
	if !q.canAdmitBytesLocked(class, int64(len(data))) {
		err := q.failureLocked(class, SendQueueByteLimit, 1, int64(len(data)))
		q.signalFatalLocked(class, err)
		return err
	}
	critical := outboundClassIsCritical(class)
	if !q.budget.reserve(int64(len(data)), critical) {
		err := q.failureLocked(class, SendQueueSharedByteLimit, 1, int64(len(data)))
		q.signalFatalLocked(class, err)
		return err
	}

	owned := bytes.Clone(data)
	q.reliable = append(q.reliable, outboundFrame{data: owned, class: class})
	q.classFrames[class]++
	q.totalFrames++
	q.totalBytes += int64(len(owned))
	q.reliableBytes += int64(len(owned))
	q.signalWakeLocked()
	return nil
}

func (q *outboundQueue) enqueueBulk(frames [][]byte) error {
	batch := make([][]byte, 0, len(frames))
	var totalBytes int64
	for _, frame := range frames {
		if len(frame) == 0 {
			continue
		}
		batch = append(batch, frame)
		totalBytes += int64(len(frame))
	}
	if len(batch) == 0 {
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return q.failureLocked(OutboundClassBulk, SendQueueClosed, len(batch), totalBytes)
	}
	if len(q.bulk)-q.bulkHead >= bulkSequenceBufferSize {
		return q.failureLocked(OutboundClassBulk, SendQueueSequenceLimit, len(batch), totalBytes)
	}
	if !q.canAdmitBytesLocked(OutboundClassBulk, totalBytes) {
		return q.failureLocked(OutboundClassBulk, SendQueueByteLimit, len(batch), totalBytes)
	}
	if !q.budget.reserve(totalBytes, false) {
		return q.failureLocked(OutboundClassBulk, SendQueueSharedByteLimit, len(batch), totalBytes)
	}

	owned := make([][]byte, len(batch))
	for i, frame := range batch {
		owned[i] = bytes.Clone(frame)
	}
	q.bulk = append(q.bulk, outboundSequence{frames: owned})
	q.classFrames[OutboundClassBulk] += len(owned)
	q.totalFrames += len(owned)
	q.totalBytes += totalBytes
	q.bulkBytes += totalBytes
	q.signalWakeLocked()
	return nil
}

func (q *outboundQueue) canAdmitReliableLocked(class OutboundSendClass) bool {
	if class >= OutboundClassBulk || q.classFrames[class] >= outboundClassMaximum(class) {
		return false
	}

	reliableFrames := 0
	for candidate := OutboundClassControl; candidate <= OutboundClassOrdinary; candidate++ {
		reliableFrames += q.classFrames[candidate]
	}
	if reliableFrames >= reliableSendBufferSize {
		return false
	}

	freeAfter := reliableSendBufferSize - reliableFrames - 1
	protected := 0
	for candidate := OutboundClassControl; candidate <= OutboundClassOrdinary; candidate++ {
		if candidate == class {
			continue
		}
		if deficit := outboundClassMinimum(candidate) - q.classFrames[candidate]; deficit > 0 {
			protected += deficit
		}
	}
	return freeAfter >= protected
}

func (q *outboundQueue) canAdmitBytesLocked(class OutboundSendClass, bytes int64) bool {
	if bytes < 0 {
		return false
	}
	limit := int64(outboundNonCriticalByteMaximum)
	if class == OutboundClassControl || class == OutboundClassConsensus {
		limit = int64(outboundRetainedByteMaximum)
	}
	return bytes <= limit-q.totalBytes
}

func outboundClassIsCritical(class OutboundSendClass) bool {
	return class == OutboundClassControl || class == OutboundClassConsensus
}

func outboundClassMinimum(class OutboundSendClass) int {
	switch class {
	case OutboundClassControl:
		return controlSendMinimum
	case OutboundClassConsensus:
		return consensusSendMinimum
	case OutboundClassAcquisition:
		return acquisitionSendMinimum
	case OutboundClassOrdinary:
		return ordinarySendMinimum
	default:
		return 0
	}
}

func outboundClassMaximum(class OutboundSendClass) int {
	switch class {
	case OutboundClassControl:
		return controlSendMaximum
	case OutboundClassConsensus:
		return consensusSendMaximum
	case OutboundClassAcquisition:
		return acquisitionSendMaximum
	case OutboundClassOrdinary:
		return ordinarySendMaximum
	default:
		return 0
	}
}

func (q *outboundQueue) failureLocked(
	class OutboundSendClass,
	reason SendQueueFailureReason,
	frames int,
	bytes int64,
) error {
	return &SendQueueError{
		Class:           class,
		Reason:          reason,
		AttemptedFrames: frames,
		AttemptedBytes:  bytes,
		RetainedFrames:  q.totalFrames,
		RetainedBytes:   q.totalBytes,
	}
}

func (q *outboundQueue) signalFatalLocked(class OutboundSendClass, err error) {
	if class != OutboundClassControl && class != OutboundClassConsensus {
		return
	}
	select {
	case q.fatal <- err:
		if queueErr, ok := err.(*SendQueueError); ok && q.onFatal != nil {
			q.onFatal(queueErr)
		}
	default:
	}
}

func (q *outboundQueue) signalWakeLocked() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *outboundQueue) next(ctx context.Context) (outboundToken, error) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return outboundToken{}, ErrConnectionClosed
		}
		if q.inFlight == nil {
			if token, ok := q.nextLocked(); ok {
				q.inFlight = &token
				q.mu.Unlock()
				return token, nil
			}
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return outboundToken{}, ctx.Err()
		case <-q.wake:
		}
	}
}

func (q *outboundQueue) nextLocked() (outboundToken, bool) {
	hasReliable := q.reliableHead < len(q.reliable)
	hasBulk := q.bulkHead < len(q.bulk)
	if !hasReliable && !hasBulk {
		return outboundToken{}, false
	}

	if hasBulk && (!hasReliable || q.reliableBurst >= reliableFramesPerBulkFrame) {
		sequence := &q.bulk[q.bulkHead]
		frame := sequence.frames[sequence.next]
		sequence.frames[sequence.next] = nil
		sequence.next++
		q.bulkBytes -= int64(len(frame))
		q.reliableBurst = 0
		return q.newTokenLocked(frame, OutboundClassBulk), true
	}

	frame := q.reliable[q.reliableHead]
	q.reliable[q.reliableHead] = outboundFrame{}
	q.reliableHead++
	q.reliableBytes -= int64(len(frame.data))
	if q.reliableBurst < reliableFramesPerBulkFrame {
		q.reliableBurst++
	}
	q.compactReliableLocked()
	return q.newTokenLocked(frame.data, frame.class), true
}

func (q *outboundQueue) newTokenLocked(data []byte, class OutboundSendClass) outboundToken {
	q.nextTokenID++
	return outboundToken{id: q.nextTokenID, data: data, class: class}
}

func (q *outboundQueue) complete(token outboundToken) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.inFlight == nil || q.inFlight.id != token.id {
		return
	}
	q.totalFrames--
	q.totalBytes -= int64(len(token.data))
	q.classFrames[token.class]--
	critical := outboundClassIsCritical(token.class)
	q.budget.release(int64(len(token.data)), critical)
	q.inFlight = nil

	if token.class == OutboundClassBulk && q.bulkHead < len(q.bulk) {
		sequence := &q.bulk[q.bulkHead]
		if sequence.next == len(sequence.frames) {
			*sequence = outboundSequence{}
			q.bulkHead++
			q.compactBulkLocked()
		}
	}
	q.signalWakeLocked()
}

func (q *outboundQueue) compactReliableLocked() {
	if q.reliableHead == len(q.reliable) {
		q.reliable = q.reliable[:0]
		q.reliableHead = 0
		return
	}
	if q.reliableHead >= 256 && q.reliableHead*2 >= len(q.reliable) {
		copy(q.reliable, q.reliable[q.reliableHead:])
		q.reliable = q.reliable[:len(q.reliable)-q.reliableHead]
		q.reliableHead = 0
	}
}

func (q *outboundQueue) compactBulkLocked() {
	if q.bulkHead == len(q.bulk) {
		q.bulk = q.bulk[:0]
		q.bulkHead = 0
		return
	}
	if q.bulkHead >= bulkSequenceBufferSize {
		copy(q.bulk, q.bulk[q.bulkHead:])
		q.bulk = q.bulk[:len(q.bulk)-q.bulkHead]
		q.bulkHead = 0
	}
}

func (q *outboundQueue) snapshot() outboundQueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	snapshot := outboundQueueSnapshot{
		ReliableQueued: len(q.reliable) - q.reliableHead,
		ReliableBytes:  q.reliableBytes,
		BulkSequences:  len(q.bulk) - q.bulkHead,
		BulkBytes:      q.bulkBytes,
		TotalFrames:    q.totalFrames,
		TotalBytes:     q.totalBytes,
		ClassFrames:    q.classFrames,
	}
	for i := q.bulkHead; i < len(q.bulk); i++ {
		snapshot.BulkQueued += len(q.bulk[i].frames) - q.bulk[i].next
	}
	if q.inFlight != nil {
		snapshot.InFlight = 1
		snapshot.InFlightBytes = int64(len(q.inFlight.data))
	}
	return snapshot
}

func (q *outboundQueue) fatalSignal() <-chan error {
	return q.fatal
}

func (q *outboundQueue) close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.budget.close()
	q.budget = nil

	for i := range q.reliable {
		q.reliable[i] = outboundFrame{}
	}
	for i := range q.bulk {
		q.bulk[i] = outboundSequence{}
	}
	q.reliable = nil
	q.bulk = nil
	q.reliableHead = 0
	q.bulkHead = 0
	q.inFlight = nil
	q.classFrames = [outboundClassCount]int{}
	q.totalFrames = 0
	q.totalBytes = 0
	q.reliableBytes = 0
	q.bulkBytes = 0
	q.signalWakeLocked()
	q.mu.Unlock()
}

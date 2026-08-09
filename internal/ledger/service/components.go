package service

import (
	"context"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

type persistenceWorker struct {
	service *Service
	// persistMu owns queue lifecycle; canonicalPersistMu serializes tip replacement.
	persistMu            sync.Mutex
	persistQueue         []*persistJob
	validatedPersistJobs map[uint32]*persistJob
	persistWake          chan struct{}
	persistStarted       bool
	persistStopping      bool
	persistWG            sync.WaitGroup
	canonicalPersistMu   sync.Mutex
}

type eventPublisher struct {
	service *Service
	// ledgerEventMu owns dispatcher lifecycle, ordering, and queue state.
	ledgerEventMu           sync.Mutex
	publicationQueue        []publicationEvent
	publicationLimit        int
	publicationFailed       bool
	serverStatusQueued      bool
	publicationErrors       chan error
	ledgerEventCandidates   map[uint32]*LedgerAcceptedEvent
	ledgerEventFrontierSeq  uint32
	ledgerEventFrontierHash [32]byte
	ledgerEventHaveFrontier bool
	ledgerEventWake         chan struct{}
	ledgerEventStarted      bool
	ledgerEventStopping     bool
	ledgerEventWG           sync.WaitGroup
	publicationFailureOnce  sync.Once
	subscriberMu            sync.RWMutex
	eventSink               EventSink
	submittedTxCallback     SubmittedTxCallback
	serverStatusCallback    ServerStatusCallback
}

type historyComponent struct {
	// mu owns ledger history, hash/cache indexes, and transaction indexes.
	// Operations that also change the open/closed/validated frontier take
	// Service.mu first, then mu. History-only queries take only mu.
	mu                   sync.RWMutex
	ledgerHistory        map[uint32]*ledger.Ledger
	ledgerByHash         map[[32]byte]uint32
	persistedLedgers     map[[32]byte]*ledger.Ledger
	persistedLedgerFIFO  [][32]byte
	txIndex              map[[32]byte]uint32
	txPositionIndex      map[[32]byte]uint32
	completeMu           sync.RWMutex
	completedLedgers     *completeLedgerSet
	completeLedgerHashes map[uint32][32]byte
	completeLedgerFloor  uint32
	completeLedgerTokens map[uint32]uint64

	nextCompleteLedgerToken uint64
	sweepMu                 sync.Mutex
	sweepCancel             context.CancelFunc
	sweepDone               chan struct{}
	sweepInterval           time.Duration
}

type queryFacade struct {
	service      *Service
	history      *historyComponent
	relationalDB relationaldb.RepositoryManager
}

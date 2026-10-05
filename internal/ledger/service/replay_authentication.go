package service

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

func (s *Service) SetReplayEvidenceAuthenticator(capture func(header.LedgerHeader) (json.RawMessage, error), verify func(header.LedgerHeader, json.RawMessage) error) {
	s.mu.Lock()
	s.replayEvidenceCapture = capture
	s.replayEvidenceVerify = verify
	s.mu.Unlock()
}

func (s *Service) authenticateReplayEvidence(evidence replayEvidence) bool {
	if len(evidence.Authentication) == 0 {
		return evidence.Authenticated
	}
	s.mu.RLock()
	verify := s.replayEvidenceVerify
	s.mu.RUnlock()
	return verify != nil && verify(evidence.Target, evidence.Authentication) == nil
}

package service

import "context"

func (s *Service) WaitForSpeculationForTest(ctx context.Context) error {
	return s.waitSpeculativeSuccessor(ctx)
}

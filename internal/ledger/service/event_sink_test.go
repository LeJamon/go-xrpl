package service

func setEventSinkFunc(s *Service, sink func(*LedgerAcceptedEvent)) {
	s.SetEventSink(EventSinkFunc(sink))
}

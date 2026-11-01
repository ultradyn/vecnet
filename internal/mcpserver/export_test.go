package mcpserver

// HeldHints reports how many URIs of session sid have a hint waiting for a
// GET stream.
func (s *Server) HeldHints(sid string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[sid]; sess != nil && sess.res != nil {
		return len(sess.res.pending)
	}
	return 0
}

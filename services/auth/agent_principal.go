package auth

import "github.com/fanlv/quartet/types/model"

// PrincipalForExecution rechecks the current user's status and permissions for
// a backend-owned agent execution; it does not create a browser session.
func (s *Service) PrincipalForExecution(userID string) (*model.AuthPrincipal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireReadyLocked(); err != nil {
		return nil, err
	}
	user := s.users[userID]
	if user == nil || user.Status != UserStatusActive || user.MustChangePassword {
		return nil, ErrUnauthorized
	}
	return s.principalLocked(user, "")
}

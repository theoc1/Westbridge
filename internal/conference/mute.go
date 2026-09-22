package conference

import (
	"context"
	"fmt"

	"github.com/dmalkin/westbridge/internal/ami"
)

// SetMuted controls the participant's microphone, preserving conference audio to them.
func (s *Service) SetMuted(ctx context.Context, uniqueID string, muted bool) error {
	p, ok := s.roster.Get(uniqueID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrParticipantNotFound, uniqueID)
	}
	name := "ConfbridgeMute"
	if !muted {
		name = "ConfbridgeUnmute"
	}
	action := ami.NewAction(name)
	action.Add("Conference", s.cfg.Room)
	action.Add("Channel", p.Channel)
	if _, err := s.client.Action(ctx, action); err != nil {
		return fmt.Errorf("set mute %s: %w", p.Channel, err)
	}
	// Read the actual state instead of optimistically overriding a concurrent
	// operator's action. Events and periodic resync recover a failed refresh.
	if _, err := s.List(ctx); err != nil {
		s.log.Warn("conference: mute refresh failed", "error", err)
	}
	return nil
}

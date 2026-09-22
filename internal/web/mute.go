package web

import (
	"context"
	"net/http"
)

func (s *Server) handleMuteParticipant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Muted *bool `json:"muted"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	if req.Muted == nil {
		s.writeError(r.Context(), w, 400, "muted must be a boolean")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()
	if err := s.svc.SetMuted(ctx, r.PathValue("uniqueid"), *req.Muted); err != nil {
		s.writeServiceError(ctx, w, "mute", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

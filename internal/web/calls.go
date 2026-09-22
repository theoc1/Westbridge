package web

import (
	"context"
	"net/http"
)

func (s *Server) handleCancelCall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()
	if err := s.svc.CancelCall(ctx, r.PathValue("id")); err != nil {
		s.writeServiceError(ctx, w, "cancel call", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleRetryCall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()
	id, err := s.svc.RetryCall(ctx, r.PathValue("id"))
	if err != nil {
		s.writeServiceError(ctx, w, "retry call", err)
		return
	}
	writeJSON(ctx, s.log, w, http.StatusAccepted, addParticipantResponse{ActionID: id})
}

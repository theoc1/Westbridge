package web

import (
	"errors"
	"github.com/dmalkin/westbridge/internal/settings"
	"net/http"
)

// The locale is public so that the login screen uses the installation language.
func (s *Server) handleLocale(w http.ResponseWriter, r *http.Request) {
	locale := "en"
	if s.cfg.Settings != nil {
		var err error
		locale, err = s.cfg.Settings.Locale(r.Context())
		if err != nil {
			s.writeError(r.Context(), w, 500, "could not load language")
			return
		}
	}
	writeJSON(r.Context(), s.log, w, 200, map[string]string{"locale": locale})
}
func (s *Server) handleSetLocale(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Locale string `json:"locale"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	if s.cfg.Settings == nil {
		s.writeError(r.Context(), w, 503, "settings unavailable")
		return
	}
	if err := s.cfg.Settings.SetLocale(r.Context(), body.Locale); err != nil {
		if errors.Is(err, settings.ErrInvalidLocale) {
			s.writeError(r.Context(), w, 400, err.Error())
		} else {
			s.writeError(r.Context(), w, 500, "could not save language")
		}
		return
	}
	writeJSON(r.Context(), s.log, w, 200, body)
}

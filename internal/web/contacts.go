package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dmalkin/westbridge/internal/auth"
)

func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET", "HEAD":
		contacts, err := s.cfg.Auth.Contacts(r.Context(), r.Context().Value(userKey{}).(auth.User).ID)
		if err != nil {
			s.writeContactError(w, r, err)
			return
		}
		writeJSON(r.Context(), s.log, w, 200, contacts)
	case "POST":
		s.saveContact(w, r, 0)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		s.writeError(r.Context(), w, 405, "method not allowed")
	}
}

func (s *Server) handleContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(r.Context(), w, 400, "invalid contact id")
		return
	}
	switch r.Method {
	case "PUT":
		s.saveContact(w, r, id)
	case "DELETE":
		if err := s.cfg.Auth.DeleteContact(r.Context(), r.Context().Value(userKey{}).(auth.User).ID, id); err != nil {
			s.writeContactError(w, r, err)
			return
		}
		w.WriteHeader(204)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		s.writeError(r.Context(), w, 405, "method not allowed")
	}
}

func (s *Server) saveContact(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	contact, err := s.cfg.Auth.SaveContact(r.Context(), r.Context().Value(userKey{}).(auth.User).ID, id, req.Name, req.Number)
	if err != nil {
		s.writeContactError(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	writeJSON(r.Context(), s.log, w, status, contact)
}

func (s *Server) writeContactError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrContactInvalid):
		s.writeError(r.Context(), w, 400, err.Error())
	case errors.Is(err, auth.ErrContactMissing):
		s.writeError(r.Context(), w, 404, err.Error())
	case errors.Is(err, auth.ErrContactDuplicate):
		s.writeError(r.Context(), w, 409, err.Error())
	default:
		s.log.Error("phonebook request failed", "error", err)
		s.writeError(r.Context(), w, 500, "could not access phonebook")
	}
}

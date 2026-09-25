package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/rooms"
)

type roomServers struct {
	mu      sync.Mutex
	servers map[string]*Server
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	list, err := s.cfg.Rooms.All(r.Context())
	if err != nil {
		s.writeRoomError(w, r, err)
		return
	}
	out := []rooms.Room{}
	user := r.Context().Value(userKey{}).(auth.User)
	for _, room := range list {
		ok, err := s.cfg.Rooms.Allowed(r.Context(), room.ID, user.ID)
		if err != nil {
			s.writeRoomError(w, r, err)
			return
		}
		if ok {
			if user.Role != "admin" {
				room.Error = ""
			}
			out = append(out, room)
		}
	}
	writeJSON(r.Context(), s.log, w, 200, out)
}

func (s *Server) handleAdminRooms(w http.ResponseWriter, r *http.Request) {
	s.cfg.Manager.Admission.Lock()
	defer s.cfg.Manager.Admission.Unlock()
	cookie, _ := r.Cookie(sessionCookie)
	user, err := s.cfg.Auth.Authenticate(cookie.Value)
	if err != nil || user.Role != "admin" {
		s.writeError(r.Context(), w, 403, "administrator access required")
		return
	}
	id := r.PathValue("roomID")
	if id == "" {
		switch r.Method {
		case "GET":
			s.handleRooms(w, r)
		case "POST":
			var body struct {
				Name   string `json:"name"`
				Number string `json:"number"`
			}
			if !s.decodeBody(w, r, &body) {
				return
			}
			room, err := s.cfg.Rooms.Create(r.Context(), body.Name, body.Number)
			if err != nil {
				s.writeRoomError(w, r, err)
				return
			}
			s.cfg.Manager.Runtime(room)
			writeJSON(r.Context(), s.log, w, 202, room)
		default:
			s.writeError(r.Context(), w, 405, "method not allowed")
		}
		return
	}
	room, err := s.cfg.Rooms.Get(r.Context(), id)
	if err != nil || room.State == "deleted" {
		s.writeRoomError(w, r, rooms.ErrMissing)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/users") {
		switch r.Method {
		case "GET":
			users, err := s.cfg.Rooms.Grants(r.Context(), id)
			if err != nil {
				s.writeRoomError(w, r, err)
				return
			}
			writeJSON(r.Context(), s.log, w, 200, users)
		case "PUT":
			var body struct {
				UserIDs []int64 `json:"userIds"`
			}
			if !s.decodeBody(w, r, &body) {
				return
			}
			if err := s.cfg.Rooms.SetGrants(r.Context(), id, body.UserIDs); err != nil {
				s.writeRoomError(w, r, err)
				return
			}
			w.WriteHeader(204)
		default:
			s.writeError(r.Context(), w, 405, "method not allowed")
		}
		return
	}
	if r.Method != "DELETE" {
		s.writeError(r.Context(), w, 405, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()
	if err := s.cfg.Manager.Delete(ctx, room); err != nil {
		s.writeRoomError(w, r, err)
		return
	}
	room, err = s.cfg.Rooms.Get(r.Context(), id)
	if err != nil {
		s.writeRoomError(w, r, err)
		return
	}
	writeJSON(r.Context(), s.log, w, 202, room)
}

func (s *Server) roomServer(room rooms.Room) (*Server, error) {
	s.children.mu.Lock()
	defer s.children.mu.Unlock()
	if child := s.children.servers[room.ID]; child != nil {
		return child, nil
	}
	cfg := s.cfg
	cfg.Manager = nil
	cfg.RoomID = room.ID
	cfg.RoomName = room.Name
	cfg.RoomNumber = room.Number
	cfg.AccessLock = &s.cfg.Manager.Admission
	cfg.RoomAccess = func(ctx context.Context, user auth.User) bool {
		ok, err := s.cfg.Rooms.Allowed(ctx, room.ID, user.ID)
		return err == nil && ok
	}
	child, err := New(s.cfg.Manager.Runtime(room), cfg)
	if err != nil {
		return nil, err
	}
	s.children.servers[room.ID] = child
	return child, nil
}

func (s *Server) handleRoomRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("roomID")
	socket := r.URL.Path == "/ws"
	if socket {
		id = r.URL.Query().Get("roomId")
	}
	s.cfg.Manager.Admission.RLock()
	unlock := true
	defer func() {
		if unlock {
			s.cfg.Manager.Admission.RUnlock()
		}
	}()
	user := r.Context().Value(userKey{}).(auth.User)
	ok, err := s.cfg.Rooms.Allowed(r.Context(), id, user.ID)
	if err != nil {
		s.writeRoomError(w, r, err)
		return
	}
	if !ok {
		s.writeRoomError(w, r, rooms.ErrMissing)
		return
	}
	room, err := s.cfg.Rooms.Get(r.Context(), id)
	if err != nil {
		s.writeRoomError(w, r, err)
		return
	}
	if r.Method == "POST" {
		if err := s.cfg.Manager.Ready(room); err != nil {
			s.writeRoomError(w, r, err)
			return
		}
	}
	child, err := s.roomServer(room)
	if err != nil {
		s.writeRoomError(w, r, err)
		return
	}
	if socket {
		s.cfg.Manager.Admission.RUnlock()
		unlock = false
		child.handleWebSocket(w, r)
		return
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/conference"):
		child.handleGetConference(w, r)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/participants"):
		child.handleAddParticipant(w, r)
	case r.Method == "DELETE" && r.PathValue("uniqueid") != "":
		child.handleKickParticipant(w, r)
	case r.Method == "PUT" && r.PathValue("uniqueid") != "":
		child.handleMuteParticipant(w, r)
	case r.Method == "DELETE" && r.PathValue("id") != "":
		child.handleCancelCall(w, r)
	case r.Method == "POST" && r.PathValue("id") != "":
		child.handleRetryCall(w, r)
	default:
		s.writeError(r.Context(), w, 405, "method not allowed")
	}
}

func (s *Server) writeRoomError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, rooms.ErrMissing):
		s.writeError(r.Context(), w, 404, err.Error())
	case errors.Is(err, rooms.ErrInvalid):
		s.writeError(r.Context(), w, 400, err.Error())
	case errors.Is(err, rooms.ErrBusy), errors.Is(err, rooms.ErrConflict):
		s.writeError(r.Context(), w, 409, err.Error())
	case errors.Is(err, rooms.ErrNotReady):
		s.writeError(r.Context(), w, 503, err.Error())
	default:
		s.writeServiceError(r.Context(), w, "room operation", err)
	}
}

func (s *Server) pruneRooms() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			list, err := s.cfg.Rooms.All(context.Background())
			if err != nil {
				continue
			}
			s.children.mu.Lock()
			for _, room := range list {
				if room.State == "deleted" {
					if child := s.children.servers[room.ID]; child != nil {
						child.Close()
						delete(s.children.servers, room.ID)
					}
				}
			}
			s.children.mu.Unlock()
		}
	}
}

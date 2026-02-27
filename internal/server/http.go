package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go-agent/internal/app"
)

type HTTPServer struct {
	app *app.App
}

func New(a *app.App) *HTTPServer {
	return &HTTPServer{app: a}
}

func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/sessions", s.handleSessions)
	mux.HandleFunc("/sessions/", s.handleSessionSub)
	return mux
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *HTTPServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		ss, err := s.app.CreateSession(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ss)
	case http.MethodGet:
		limit := 30
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		items, err := s.app.ListSessionIDs(r.Context(), limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *HTTPServer) handleSessionSub(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/sessions/")
	path = strings.Trim(path, "/")
	if path == "" {
		writeErr(w, http.StatusBadRequest, "session id required")
		return
	}
	parts := strings.Split(path, "/")
	sid := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ss, err := s.app.GetSession(r.Context(), sid)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ss)
		return
	}

	switch parts[1] {
	case "turns":
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Input string `json:"input"`
			Agent string `json:"agent,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := s.app.HandleTurn(r.Context(), app.TurnRequest{
			SessionID: sid,
			Input:     req.Input,
			Agent:     req.Agent,
		}, nil)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case "approvals":
		if len(parts) != 3 {
			writeErr(w, http.StatusBadRequest, "approval id required")
			return
		}
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Approve bool `json:"approve"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		apr, err := s.app.ResolveApproval(r.Context(), parts[2], req.Approve)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, apr)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

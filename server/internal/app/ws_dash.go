package app

import (
	"net/http"

	"github.com/gorilla/websocket"

	"ov-computeruse/server/internal/protocol"
)

func (s *Server) handleDashWS(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	dash := &DashConn{ID: protocol.NewID("dash"), Principal: principal, Conn: conn, Send: make(chan []byte, 128)}
	s.hub.AddDash(dash)
	s.log.InfoContext(r.Context(), "dash connected", "dash_id", dash.ID, "user_id", principal.UserID, "admin", principal.Admin)
	go s.dashWriter(dash)
	s.dashReader(r, dash)
}

func (s *Server) dashWriter(dash *DashConn) {
	for data := range dash.Send {
		if err := dash.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

func (s *Server) dashReader(r *http.Request, dash *DashConn) {
	defer func() {
		s.hub.RemoveDash(dash.ID)
		_ = dash.Conn.Close()
		close(dash.Send)
		s.log.InfoContext(r.Context(), "dash disconnected", "dash_id", dash.ID, "user_id", dash.Principal.UserID, "admin", dash.Principal.Admin)
	}()
	for {
		if _, _, err := dash.Conn.ReadMessage(); err != nil {
			return
		}
	}
}

package app

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dash_dist
var dashDist embed.FS

func (s *Server) handleDashStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
		http.NotFound(w, r)
		return
	}
	dist, err := fs.Sub(dashDist, "dash_dist")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(dist, path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		path = "index.html"
	}
	http.ServeFileFS(w, r, dist, path)
}

func (s *Server) handleDashConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"sub2api_login_upstream": s.cfg.Sub2APILoginUpstream})
}

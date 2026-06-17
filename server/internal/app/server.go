package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"

	"ov-computeruse/server/internal/config"
	"ov-computeruse/server/internal/platform/httpx"
	"ov-computeruse/server/internal/store"
)

type Server struct {
	cfg      config.Config
	store    Repository
	hub      *Hub
	log      *slog.Logger
	bind     BindService
	sessions SessionService
}

func New(cfg config.Config, st Repository, redisClient *redis.Client, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, store: st, hub: NewHub(redisClient, st, logger), log: logger, bind: NewBindService(st, cfg.PublicURL, cfg.ServerKeyID), sessions: NewSessionService(redisClient, st, logger)}
}

func (s *Server) Run(ctx context.Context) {
	s.hub.Run(ctx)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/agents/bind", s.handleBind)
	mux.HandleFunc("POST /api/dash/login", s.handleDashLogin)
	mux.HandleFunc("GET /api/dash/me", s.handleDashMe)
	mux.HandleFunc("GET /api/dash/agents", s.handleDashAgents)
	mux.HandleFunc("GET /api/dash/commands", s.handleDashCommands)
	mux.HandleFunc("GET /api/dash/commands/{command_id}", s.handleDashCommandDetail)
	mux.HandleFunc("POST /api/dash/commands/{command_id}/retry", s.handleDashCommandRetry)
	mux.HandleFunc("GET /api/dash/projects", s.handleDashProjects)
	mux.HandleFunc("GET /api/dash/sessions", s.handleDashSessions)
	mux.HandleFunc("GET /api/dash/runs", s.handleDashRuns)
	mux.HandleFunc("GET /api/dash/runs/events", s.handleDashRunEvents)
	mux.HandleFunc("GET /api/dash/runs/timeline", s.handleDashRunTimeline)
	mux.HandleFunc("GET /api/dash/runtime-sessions", s.handleDashRuntimeSessions)
	mux.HandleFunc("GET /api/dash/history/items", s.handleDashHistoryItems)
	mux.HandleFunc("GET /api/dash/approvals", s.handleDashApprovals)
	mux.HandleFunc("POST /api/dash/approvals/{approval_id}/decision", s.handleDashApprovalDecision)
	mux.HandleFunc("GET /ws/agent", s.handleAgentWS)
	mux.HandleFunc("GET /ws/dash", s.handleDashWS)
	mux.HandleFunc("POST /api/dash/commands", s.handleDashCommand)
	mux.HandleFunc("GET /api/dash/history/messages", s.handleDashHistoryMessages)
	return httpx.Middleware(s.log, mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) requireDash(r *http.Request) (DashPrincipal, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return DashPrincipal{}, false
	}
	if token == s.cfg.DashToken {
		return DashPrincipal{Admin: true}, true
	}
	principal, err := s.sessions.Principal(r.Context(), token)
	if err != nil {
		return DashPrincipal{}, false
	}
	return principal, true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) SeedUsers(ctx context.Context, users []store.BindUser) error {
	return s.bind.SeedUsers(ctx, users)
}

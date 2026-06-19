package codexcli

import (
	"context"
	"strings"
	"sync"

	"ov-computeruse/agent/internal/protocol"
)

type activeRuns struct {
	mu        sync.Mutex
	cancelBy  map[string]context.CancelFunc
	sessionBy map[string]string
}

func (r *activeRuns) track(command protocol.Command, cancel context.CancelFunc) {
	runID := strings.TrimSpace(command.RunID)
	if runID == "" || cancel == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelBy == nil {
		r.cancelBy = map[string]context.CancelFunc{}
	}
	if r.sessionBy == nil {
		r.sessionBy = map[string]string{}
	}
	r.cancelBy[runID] = cancel
	if command.SessionID != "" {
		r.sessionBy[command.SessionID] = runID
	}
}

func (r *activeRuns) alias(runID string, sessionIDs ...string) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessionBy == nil {
		r.sessionBy = map[string]string{}
	}
	if r.cancelBy == nil || r.cancelBy[runID] == nil {
		return
	}
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID != "" {
			r.sessionBy[sessionID] = runID
		}
	}
}

func (r *activeRuns) untrack(command protocol.Command) {
	runID := strings.TrimSpace(command.RunID)
	if runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancelBy, runID)
	if command.SessionID != "" && r.sessionBy[command.SessionID] == runID {
		delete(r.sessionBy, command.SessionID)
	}
	for sessionID, sessionRunID := range r.sessionBy {
		if sessionRunID == runID {
			delete(r.sessionBy, sessionID)
		}
	}
}

func (r *activeRuns) cancel(command protocol.Command) (context.CancelFunc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runID := strings.TrimSpace(command.RunID)
	if runID == "" && command.SessionID != "" {
		runID = r.sessionBy[command.SessionID]
	}
	cancel := r.cancelBy[runID]
	return cancel, cancel != nil
}

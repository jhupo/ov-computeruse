package app

import (
	"context"
	"time"
)

const (
	commandDispatchInterval = 10 * time.Second
	commandDispatchBatch    = 200
)

func (s *Server) runCommandDispatcher(ctx context.Context) {
	ticker := time.NewTicker(commandDispatchInterval)
	defer ticker.Stop()
	for {
		s.dispatchPendingCommands(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) dispatchPendingCommands(ctx context.Context) {
	commands, err := s.store.PendingDispatchCommands(ctx, commandDispatchBatch)
	if err != nil {
		s.log.WarnContext(ctx, "pending command scan failed", "error", err)
		return
	}
	seenAgents := map[string]struct{}{}
	for _, command := range commands {
		if _, ok := seenAgents[command.AgentID]; !ok {
			seenAgents[command.AgentID] = struct{}{}
			if err := s.store.ExpireCommands(ctx, command.AgentID); err != nil {
				s.log.WarnContext(ctx, "command expiration failed", "agent_id", command.AgentID, "error", err)
			}
		}
		identity, err := s.store.AgentByID(ctx, command.AgentID)
		if err != nil {
			s.log.WarnContext(ctx, "pending command agent lookup failed", "agent_id", command.AgentID, "command_id", command.ID, "error", err)
			continue
		}
		if err := s.validateCommandTargets(ctx, command.AgentID, command.ToProtocol()); err != nil {
			_ = s.store.MarkCommandFailed(ctx, command.AgentID, command.ID, err.Error())
			s.log.WarnContext(ctx, "pending command target invalid", "agent_id", command.AgentID, "command_id", command.ID, "error", err)
			continue
		}
		record, dispatched := s.dispatchCommand(ctx, identity, command.ToProtocol())
		if dispatched {
			s.log.InfoContext(ctx, "pending command dispatched", "agent_id", command.AgentID, "command_id", command.ID, "status", record.Status)
		}
	}
}

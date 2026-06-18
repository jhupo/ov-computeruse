package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type AuditLogFilter struct {
	UserID  string
	AgentID string
	Action  string
	Since   time.Time
	Until   time.Time
	Limit   int
}

type AuditLogRecord struct {
	ID        string          `json:"id"`
	UserID    string          `json:"user_id,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	Action    string          `json:"action"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Store) ListAuditLogs(ctx context.Context, filter AuditLogFilter) ([]AuditLogRecord, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, COALESCE(user_id, ''), COALESCE(agent_id, ''), action, payload, created_at FROM audit_logs`
	args := []any{}
	where := []string{}
	if strings.TrimSpace(filter.UserID) != "" {
		args = append(args, strings.TrimSpace(filter.UserID))
		where = append(where, "user_id=$"+strconv.Itoa(len(args)))
	}
	if strings.TrimSpace(filter.AgentID) != "" {
		args = append(args, strings.TrimSpace(filter.AgentID))
		where = append(where, "agent_id=$"+strconv.Itoa(len(args)))
	}
	if strings.TrimSpace(filter.Action) != "" {
		args = append(args, strings.TrimSpace(filter.Action))
		where = append(where, "action=$"+strconv.Itoa(len(args)))
	}
	if !filter.Since.IsZero() {
		args = append(args, filter.Since.UTC())
		where = append(where, "created_at>=$"+strconv.Itoa(len(args)))
	}
	if !filter.Until.IsZero() {
		args = append(args, filter.Until.UTC())
		where = append(where, "created_at<=$"+strconv.Itoa(len(args)))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditLogRecord{}
	for rows.Next() {
		var item AuditLogRecord
		var payload []byte
		if err := rows.Scan(&item.ID, &item.UserID, &item.AgentID, &item.Action, &payload, &item.CreatedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

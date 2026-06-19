package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) SaveHistoryItems(ctx context.Context, agentID string, batch protocol.HistoryItems) error {
	if shouldStageHistoryItems(batch) {
		return s.stageHistoryItems(ctx, agentID, batch)
	}
	return s.saveHistoryItemsDirect(ctx, agentID, batch)
}

func shouldStageHistoryItems(batch protocol.HistoryItems) bool {
	return strings.TrimSpace(batch.UploadID) != "" && batch.BatchCount > 1
}

func (s *Store) saveHistoryItemsDirect(ctx context.Context, agentID string, batch protocol.HistoryItems) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := saveHistoryItemsDirectTx(ctx, tx, agentID, batch); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func saveHistoryItemsDirectTx(ctx context.Context, tx pgx.Tx, agentID string, batch protocol.HistoryItems) error {
	if batch.Reset && batch.SessionID != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM history_items WHERE agent_id=$1 AND session_id=$2 AND source='codex.history'`, agentID, batch.SessionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM history_messages WHERE agent_id=$1 AND session_id=$2`, agentID, batch.SessionID); err != nil {
			return err
		}
	}
	for _, item := range batch.Items {
		if item.SessionID == "" {
			item.SessionID = batch.SessionID
		}
		if item.SessionID == "" || item.Kind == "" || protocol.IsUsageKind(item.Kind) {
			continue
		}
		source := item.Source
		if source == "" {
			source = "codex.history"
		}
		if err := insertHistoryItemTx(ctx, tx, HistoryItem{
			AgentID:       agentID,
			SessionID:     item.SessionID,
			Index:         item.Index,
			Role:          item.Role,
			Kind:          item.Kind,
			Text:          item.Text,
			Payload:       item.Payload,
			Source:        source,
			SourceEventID: item.SourceEventID,
			At:            item.At,
		}); err != nil {
			return err
		}
		if item.Kind == "message" && item.Text != "" {
			if err := insertHistoryMessageTx(ctx, tx, agentID, item.SessionID, item.Index, item.Role, item.Text, item.At); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) stageHistoryItems(ctx context.Context, agentID string, batch protocol.HistoryItems) error {
	normalized, err := normalizeHistoryItemsBatch(batch)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := upsertHistoryUpload(ctx, tx, agentID, normalized); err != nil {
		return err
	}
	if err := replaceHistoryUploadBatch(ctx, tx, agentID, normalized); err != nil {
		return err
	}
	ready, err := historyUploadReady(ctx, tx, agentID, normalized)
	if err != nil {
		return err
	}
	if ready {
		if err := commitHistoryUpload(ctx, tx, agentID, normalized); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func normalizeHistoryItemsBatch(batch protocol.HistoryItems) (protocol.HistoryItems, error) {
	batch.SessionID = strings.TrimSpace(batch.SessionID)
	batch.UploadID = strings.TrimSpace(batch.UploadID)
	if batch.SessionID == "" {
		return batch, errors.New("history items session_id is required")
	}
	if batch.UploadID == "" {
		return batch, errors.New("history items upload_id is required")
	}
	if batch.BatchCount <= 0 {
		return batch, errors.New("history items batch_count is invalid")
	}
	if batch.BatchIndex < 0 || batch.BatchIndex >= batch.BatchCount {
		return batch, errors.New("history items batch_index is invalid")
	}
	return batch, nil
}

func upsertHistoryUpload(ctx context.Context, tx pgx.Tx, agentID string, batch protocol.HistoryItems) error {
	_, err := tx.Exec(ctx, `INSERT INTO history_item_uploads (agent_id, session_id, upload_id, cursor, batch_count, final_seen, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,now(),now())
		ON CONFLICT (agent_id, session_id, upload_id) DO UPDATE SET cursor=EXCLUDED.cursor, batch_count=GREATEST(history_item_uploads.batch_count, EXCLUDED.batch_count), final_seen=history_item_uploads.final_seen OR EXCLUDED.final_seen, updated_at=now()`,
		agentID, batch.SessionID, batch.UploadID, batch.Cursor, batch.BatchCount, batch.Final)
	return err
}

func replaceHistoryUploadBatch(ctx context.Context, tx pgx.Tx, agentID string, batch protocol.HistoryItems) error {
	if _, err := tx.Exec(ctx, `DELETE FROM history_item_staging WHERE agent_id=$1 AND session_id=$2 AND upload_id=$3 AND batch_index=$4`, agentID, batch.SessionID, batch.UploadID, batch.BatchIndex); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO history_item_upload_batches (agent_id, session_id, upload_id, batch_index, received_at)
		VALUES ($1,$2,$3,$4,now())
		ON CONFLICT (agent_id, session_id, upload_id, batch_index) DO UPDATE SET received_at=now()`, agentID, batch.SessionID, batch.UploadID, batch.BatchIndex); err != nil {
		return err
	}
	for _, item := range batch.Items {
		if item.SessionID == "" {
			item.SessionID = batch.SessionID
		}
		if item.SessionID == "" || item.Kind == "" || protocol.IsUsageKind(item.Kind) {
			continue
		}
		source := item.Source
		if source == "" {
			source = "codex.history"
		}
		_, err := tx.Exec(ctx, `INSERT INTO history_item_staging (agent_id, session_id, upload_id, batch_index, item_index, role, kind, text, payload, source, source_event_id, item_at, received_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
			ON CONFLICT (agent_id, session_id, upload_id, batch_index, item_index, kind) DO UPDATE SET role=EXCLUDED.role, text=EXCLUDED.text, payload=EXCLUDED.payload, source=EXCLUDED.source, source_event_id=EXCLUDED.source_event_id, item_at=EXCLUDED.item_at, received_at=now()`,
			agentID, batch.SessionID, batch.UploadID, batch.BatchIndex, item.Index, item.Role, item.Kind, item.Text, jsonRaw(item.Payload), source, item.SourceEventID, item.At)
		if err != nil {
			return err
		}
	}
	return nil
}

func historyUploadReady(ctx context.Context, tx pgx.Tx, agentID string, batch protocol.HistoryItems) (bool, error) {
	var finalSeen bool
	var batchCount int
	var received int
	err := tx.QueryRow(ctx, `SELECT final_seen, batch_count,
		(SELECT COUNT(*) FROM history_item_upload_batches b WHERE b.agent_id=u.agent_id AND b.session_id=u.session_id AND b.upload_id=u.upload_id)
		FROM history_item_uploads u WHERE agent_id=$1 AND session_id=$2 AND upload_id=$3`,
		agentID, batch.SessionID, batch.UploadID).Scan(&finalSeen, &batchCount, &received)
	if err != nil {
		return false, err
	}
	return finalSeen && batchCount > 0 && received >= batchCount, nil
}

func commitHistoryUpload(ctx context.Context, tx pgx.Tx, agentID string, batch protocol.HistoryItems) error {
	if _, err := tx.Exec(ctx, `DELETE FROM history_items WHERE agent_id=$1 AND session_id=$2 AND source='codex.history'`, agentID, batch.SessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM history_messages WHERE agent_id=$1 AND session_id=$2`, agentID, batch.SessionID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT item_index, role, kind, text, payload, source, source_event_id, item_at
		FROM history_item_staging
		WHERE agent_id=$1 AND session_id=$2 AND upload_id=$3
		ORDER BY batch_index, item_index`, agentID, batch.SessionID, batch.UploadID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item HistoryItem
		var payload []byte
		item.AgentID = agentID
		item.SessionID = batch.SessionID
		if err := rows.Scan(&item.Index, &item.Role, &item.Kind, &item.Text, &payload, &item.Source, &item.SourceEventID, &item.At); err != nil {
			return err
		}
		if len(payload) > 0 {
			item.Payload = append(item.Payload[:0], payload...)
		}
		if err := insertHistoryItemTx(ctx, tx, item); err != nil {
			return err
		}
		if item.Kind == "message" && item.Text != "" {
			if err := insertHistoryMessageTx(ctx, tx, agentID, batch.SessionID, item.Index, item.Role, item.Text, item.At); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM history_item_staging WHERE agent_id=$1 AND session_id=$2 AND upload_id=$3`, agentID, batch.SessionID, batch.UploadID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM history_item_upload_batches WHERE agent_id=$1 AND session_id=$2 AND upload_id=$3`, agentID, batch.SessionID, batch.UploadID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM history_item_uploads WHERE agent_id=$1 AND session_id=$2 AND upload_id=$3`, agentID, batch.SessionID, batch.UploadID)
	return err
}

func insertHistoryItemTx(ctx context.Context, tx pgx.Tx, item HistoryItem) error {
	if protocol.IsUsageKind(item.Kind) {
		return nil
	}
	if item.ID == "" {
		item.ID = projectionID(item.AgentID, item.SessionID, strconv.Itoa(item.Index), item.Kind, item.SourceEventID)
	}
	if item.At.IsZero() {
		item.At = time.Now().UTC()
	}
	_, err := tx.Exec(ctx, `INSERT INTO history_items (id, agent_id, session_id, item_index, role, kind, text, payload, source, source_event_id, item_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (agent_id, session_id, item_index, kind) DO UPDATE SET role=EXCLUDED.role, text=EXCLUDED.text, payload=EXCLUDED.payload, source=EXCLUDED.source, source_event_id=EXCLUDED.source_event_id, item_at=EXCLUDED.item_at, received_at=now()`,
		item.ID, item.AgentID, item.SessionID, item.Index, item.Role, item.Kind, item.Text, jsonRaw(item.Payload), item.Source, item.SourceEventID, item.At)
	return err
}

func insertHistoryMessageTx(ctx context.Context, tx pgx.Tx, agentID, sessionID string, index int, role, text string, at time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO history_messages (agent_id, session_id, message_index, role, text, message_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (agent_id, session_id, message_index) DO UPDATE SET role=EXCLUDED.role, text=EXCLUDED.text, message_at=EXCLUDED.message_at, received_at=now()`,
		agentID, sessionID, index, role, text, at)
	return err
}

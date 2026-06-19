package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"ov-computeruse/server/internal/security"
)

type UserRecord struct {
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	KeyCount       int       `json:"key_count"`
	AgentCount     int       `json:"agent_count"`
	Disabled       bool      `json:"disabled"`
	DisabledAt     time.Time `json:"disabled_at,omitempty"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type UserKeyRecord struct {
	ID                 string    `json:"id"`
	UserID             string    `json:"user_id"`
	Name               string    `json:"name,omitempty"`
	BaseURL            string    `json:"base_url"`
	BaseURLFingerprint string    `json:"base_url_fingerprint"`
	KeyFingerprint     string    `json:"key_fingerprint"`
	Provider           string    `json:"provider,omitempty"`
	Model              string    `json:"model,omitempty"`
	Disabled           bool      `json:"disabled"`
	DisabledAt         time.Time `json:"disabled_at,omitempty"`
	DisabledReason     string    `json:"disabled_reason,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type UserUpsert struct {
	ID                    string
	Username              string
	Password              string
	Sub2APIAccessToken    string
	Sub2APIRefreshToken   string
	Sub2APITokenType      string
	Sub2APITokenExpiresAt *time.Time
	Sub2APIBalance        *float64
	Actor                 string
}

type UserKeyUpsert struct {
	ID             string
	UserID         string
	Name           string
	BaseURL        string
	KeyFingerprint string
	Provider       string
	Model          string
	Actor          string
}

func (s *Store) ListUsers(ctx context.Context, includeDisabled bool) ([]UserRecord, error) {
	query := `SELECT u.id, u.username, COUNT(DISTINCT uk.id), COUNT(DISTINCT a.id), u.disabled_at, COALESCE(u.disabled_reason, ''), u.created_at, u.updated_at
		FROM users u
		LEFT JOIN user_keys uk ON uk.user_id = u.id
		LEFT JOIN agents a ON a.user_id = u.id`
	if !includeDisabled {
		query += ` WHERE u.disabled_at IS NULL`
	}
	query += ` GROUP BY u.id, u.username, u.disabled_at, u.disabled_reason, u.created_at, u.updated_at ORDER BY u.created_at DESC`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRecord{}
	for rows.Next() {
		item, err := scanUserRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UserByID(ctx context.Context, userID string) (UserRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT u.id, u.username, COUNT(DISTINCT uk.id), COUNT(DISTINCT a.id), u.disabled_at, COALESCE(u.disabled_reason, ''), u.created_at, u.updated_at
		FROM users u
		LEFT JOIN user_keys uk ON uk.user_id = u.id
		LEFT JOIN agents a ON a.user_id = u.id
		WHERE u.id=$1
		GROUP BY u.id, u.username, u.disabled_at, u.disabled_reason, u.created_at, u.updated_at`, userID)
	item, err := scanUserRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserRecord{}, false, nil
		}
		return UserRecord{}, false, err
	}
	return item, true, nil
}

func (s *Store) UpsertUser(ctx context.Context, input UserUpsert) (UserRecord, error) {
	userID := strings.TrimSpace(input.ID)
	username := strings.TrimSpace(input.Username)
	if username == "" {
		return UserRecord{}, errors.New("username is required")
	}
	if userID == "" {
		userID = "usr_" + randomHex(16)
	}
	if strings.TrimSpace(input.Password) == "" {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&exists); err != nil {
			return UserRecord{}, err
		}
		if !exists {
			return UserRecord{}, errors.New("password is required")
		}
		_, err := s.pool.Exec(ctx, `UPDATE users SET username=$2, updated_at=now() WHERE id=$1`, userID, username)
		if err != nil {
			return UserRecord{}, err
		}
	} else {
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return UserRecord{}, err
		}
		_, err = s.pool.Exec(ctx, `INSERT INTO users (id, username, password_hash, updated_at)
			VALUES ($1,$2,$3,now())
			ON CONFLICT (id) DO UPDATE SET username=EXCLUDED.username, password_hash=EXCLUDED.password_hash, updated_at=now()`,
			userID, username, string(passwordHash))
		if err != nil {
			return UserRecord{}, err
		}
	}
	if input.Sub2APIAccessToken != "" || input.Sub2APIRefreshToken != "" || input.Sub2APIBalance != nil {
		_, err := s.pool.Exec(ctx, `UPDATE users SET
			sub2api_access_token=COALESCE(NULLIF($2, ''), sub2api_access_token),
			sub2api_refresh_token=COALESCE(NULLIF($3, ''), sub2api_refresh_token),
			sub2api_token_type=COALESCE(NULLIF($4, ''), sub2api_token_type),
			sub2api_token_expires_at=COALESCE($5, sub2api_token_expires_at),
			sub2api_balance=COALESCE($6, sub2api_balance),
			sub2api_synced_at=now(),
			updated_at=now()
			WHERE id=$1`,
			userID,
			strings.TrimSpace(input.Sub2APIAccessToken),
			strings.TrimSpace(input.Sub2APIRefreshToken),
			strings.TrimSpace(input.Sub2APITokenType),
			input.Sub2APITokenExpiresAt,
			input.Sub2APIBalance)
		if err != nil {
			return UserRecord{}, err
		}
	}
	item, found, err := s.UserByID(ctx, userID)
	if err != nil {
		return UserRecord{}, err
	}
	if !found {
		return UserRecord{}, errors.New("user not found")
	}
	_ = s.SaveAuditLog(ctx, userID, "", "user.upserted", map[string]any{"actor": input.Actor, "username": username})
	return item, nil
}

func (s *Store) SetUserAccess(ctx context.Context, userID string, change AccessChange) (UserRecord, error) {
	reason := strings.TrimSpace(change.Reason)
	actor := strings.TrimSpace(change.Actor)
	if change.Enabled {
		_, err := s.pool.Exec(ctx, `UPDATE users SET disabled_at=NULL, disabled_reason=NULL, disabled_by=NULL, updated_at=now() WHERE id=$1`, userID)
		if err != nil {
			return UserRecord{}, err
		}
	} else {
		_, err := s.pool.Exec(ctx, `UPDATE users SET disabled_at=now(), disabled_reason=$2, disabled_by=$3, updated_at=now() WHERE id=$1`, userID, nullString(reason), nullString(actor))
		if err != nil {
			return UserRecord{}, err
		}
	}
	item, found, err := s.UserByID(ctx, userID)
	if err != nil {
		return UserRecord{}, err
	}
	if !found {
		return UserRecord{}, errors.New("user not found")
	}
	return item, nil
}

func (s *Store) ListUserKeys(ctx context.Context, userID string, includeDisabled bool) ([]UserKeyRecord, error) {
	query := `SELECT id, user_id, COALESCE(name, ''), base_url, COALESCE(base_url_fingerprint, ''), key_fingerprint, COALESCE(provider, ''), COALESCE(model, ''), disabled_at, COALESCE(disabled_reason, ''), created_at, updated_at
		FROM user_keys`
	args := []any{}
	where := []string{}
	if userID != "" {
		args = append(args, userID)
		where = append(where, "user_id=$1")
	}
	if !includeDisabled {
		where = append(where, "disabled_at IS NULL")
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserKeyRecord{}
	for rows.Next() {
		item, err := scanUserKeyRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UserKeyByID(ctx context.Context, keyID string) (UserKeyRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT id, user_id, COALESCE(name, ''), base_url, COALESCE(base_url_fingerprint, ''), key_fingerprint, COALESCE(provider, ''), COALESCE(model, ''), disabled_at, COALESCE(disabled_reason, ''), created_at, updated_at
		FROM user_keys WHERE id=$1`, keyID)
	item, err := scanUserKeyRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserKeyRecord{}, false, nil
		}
		return UserKeyRecord{}, false, err
	}
	return item, true, nil
}

func (s *Store) UpsertUserKey(ctx context.Context, input UserKeyUpsert) (UserKeyRecord, error) {
	keyID := strings.TrimSpace(input.ID)
	userID := strings.TrimSpace(input.UserID)
	keyFingerprint := strings.TrimSpace(input.KeyFingerprint)
	if userID == "" {
		return UserKeyRecord{}, errors.New("user_id is required")
	}
	if keyFingerprint == "" {
		return UserKeyRecord{}, errors.New("key_fingerprint is required")
	}
	baseURL, err := normalizeBaseURL(input.BaseURL)
	if err != nil {
		return UserKeyRecord{}, err
	}
	if keyID == "" {
		keyID = "key_" + randomHex(16)
	} else if existing, found, err := s.UserKeyByID(ctx, keyID); err != nil {
		return UserKeyRecord{}, err
	} else if found && existing.UserID != userID {
		return UserKeyRecord{}, errors.New("user key belongs to another user")
	}
	baseURLFingerprint := security.FingerprintSecret(baseURL)
	_, err = s.pool.Exec(ctx, `INSERT INTO user_keys (id, user_id, name, base_url, base_url_fingerprint, key_fingerprint, provider, model, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (id) DO UPDATE SET
			user_id=EXCLUDED.user_id,
			name=EXCLUDED.name,
			base_url=EXCLUDED.base_url,
			base_url_fingerprint=EXCLUDED.base_url_fingerprint,
			key_fingerprint=EXCLUDED.key_fingerprint,
			provider=EXCLUDED.provider,
			model=EXCLUDED.model,
			updated_at=now()`,
		keyID, userID, nullString(strings.TrimSpace(input.Name)), baseURL, baseURLFingerprint, keyFingerprint, nullString(strings.TrimSpace(input.Provider)), nullString(strings.TrimSpace(input.Model)))
	if err != nil {
		return UserKeyRecord{}, err
	}
	item, found, err := s.UserKeyByID(ctx, keyID)
	if err != nil {
		return UserKeyRecord{}, err
	}
	if !found {
		return UserKeyRecord{}, errors.New("user key not found")
	}
	_ = s.SaveAuditLog(ctx, userID, "", "user_key.upserted", map[string]any{"actor": input.Actor, "key_id": keyID, "base_url": baseURL, "key_fingerprint": keyFingerprint})
	return item, nil
}

func (s *Store) SetUserKeyAccess(ctx context.Context, keyID string, change AccessChange) (UserKeyRecord, error) {
	reason := strings.TrimSpace(change.Reason)
	actor := strings.TrimSpace(change.Actor)
	if change.Enabled {
		_, err := s.pool.Exec(ctx, `UPDATE user_keys SET disabled_at=NULL, disabled_reason=NULL, disabled_by=NULL, updated_at=now() WHERE id=$1`, keyID)
		if err != nil {
			return UserKeyRecord{}, err
		}
	} else {
		_, err := s.pool.Exec(ctx, `UPDATE user_keys SET disabled_at=now(), disabled_reason=$2, disabled_by=$3, updated_at=now() WHERE id=$1`, keyID, nullString(reason), nullString(actor))
		if err != nil {
			return UserKeyRecord{}, err
		}
	}
	item, found, err := s.UserKeyByID(ctx, keyID)
	if err != nil {
		return UserKeyRecord{}, err
	}
	if !found {
		return UserKeyRecord{}, errors.New("user key not found")
	}
	return item, nil
}

type userRecordScanner interface {
	Scan(dest ...any) error
}

func scanUserRecord(scanner userRecordScanner) (UserRecord, error) {
	var item UserRecord
	var disabledAt sql.NullTime
	var updatedAt sql.NullTime
	if err := scanner.Scan(&item.ID, &item.Username, &item.KeyCount, &item.AgentCount, &disabledAt, &item.DisabledReason, &item.CreatedAt, &updatedAt); err != nil {
		return UserRecord{}, err
	}
	if disabledAt.Valid {
		item.Disabled = true
		item.DisabledAt = disabledAt.Time
	}
	if updatedAt.Valid {
		item.UpdatedAt = updatedAt.Time
	}
	return item, nil
}

func scanUserKeyRecord(scanner userRecordScanner) (UserKeyRecord, error) {
	var item UserKeyRecord
	var disabledAt sql.NullTime
	var updatedAt sql.NullTime
	if err := scanner.Scan(&item.ID, &item.UserID, &item.Name, &item.BaseURL, &item.BaseURLFingerprint, &item.KeyFingerprint, &item.Provider, &item.Model, &disabledAt, &item.DisabledReason, &item.CreatedAt, &updatedAt); err != nil {
		return UserKeyRecord{}, err
	}
	if disabledAt.Valid {
		item.Disabled = true
		item.DisabledAt = disabledAt.Time
	}
	if updatedAt.Valid {
		item.UpdatedAt = updatedAt.Time
	}
	return item, nil
}

package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const (
	Version         = "2026-06-17"
	RuntimeCodexCLI = "codex.cli"
)

type Envelope struct {
	Version    string          `json:"version"`
	MessageID  string          `json:"message_id"`
	Type       string          `json:"type"`
	AgentID    string          `json:"agent_id,omitempty"`
	DeviceID   string          `json:"device_id,omitempty"`
	Seq        uint64          `json:"seq"`
	Timestamp  time.Time       `json:"timestamp"`
	Nonce      string          `json:"nonce"`
	KeyID      string          `json:"key_id,omitempty"`
	Encryption Encryption      `json:"encryption,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Signature  string          `json:"signature,omitempty"`
}

type DeviceInfo struct {
	InstallID    string       `json:"install_id"`
	MachineHash  string       `json:"machine_hash"`
	Hostname     string       `json:"hostname"`
	OS           string       `json:"os"`
	Arch         string       `json:"arch"`
	UsernameHash string       `json:"username_hash,omitempty"`
	AgentVersion string       `json:"agent_version"`
	InstallState InstallState `json:"install_state,omitempty"`
}

type InstallState struct {
	Installed          bool      `json:"installed"`
	ServiceRegistered  bool      `json:"service_registered"`
	ServiceRunning     bool      `json:"service_running"`
	AutostartEnabled   bool      `json:"autostart_enabled"`
	PackageType        string    `json:"package_type,omitempty"`
	Channel            string    `json:"channel,omitempty"`
	ConfigDir          string    `json:"config_dir,omitempty"`
	DataDir            string    `json:"data_dir,omitempty"`
	StatePath          string    `json:"state_path,omitempty"`
	StateDBPath        string    `json:"state_db_path,omitempty"`
	LogDir             string    `json:"log_dir,omitempty"`
	CodexHome          string    `json:"codex_home,omitempty"`
	LastStartAt        time.Time `json:"last_start_at,omitempty"`
	LastInstallCheckAt time.Time `json:"last_install_check_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
}

type Credential struct {
	BaseURLFingerprint string `json:"base_url_fingerprint"`
	KeyFingerprint     string `json:"key_fingerprint"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Source             string `json:"source,omitempty"`
}

type Capabilities struct {
	SupportsRuntime   bool     `json:"supports_runtime"`
	SupportsHistory   bool     `json:"supports_history"`
	SupportsTerminal  bool     `json:"supports_terminal"`
	SupportsGit       bool     `json:"supports_git"`
	Features          []string `json:"features,omitempty"`
	MaxConcurrentRuns int      `json:"max_concurrent_runs"`
}

type AgentRegister struct {
	AgentID      string       `json:"agent_id"`
	WorkspaceID  string       `json:"workspace_id"`
	DeviceID     string       `json:"device_id"`
	Device       DeviceInfo   `json:"device"`
	Credential   Credential   `json:"credential"`
	Capabilities Capabilities `json:"capabilities"`
}

type Root struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Exists bool   `json:"exists"`
}

type RootIndex struct {
	Roots []Root `json:"roots"`
}

type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	LastActiveAt time.Time `json:"last_active_at,omitempty"`
	HasAgentsMD  bool      `json:"has_agents_md"`
	GitBranch    string    `json:"git_branch,omitempty"`
}

type ProjectIndex struct {
	Projects []Project `json:"projects"`
}

type Session struct {
	ID            string    `json:"id"`
	IDSource      string    `json:"id_source,omitempty"`
	ProjectID     string    `json:"project_id,omitempty"`
	Title         string    `json:"title,omitempty"`
	Path          string    `json:"path"`
	CWD           string    `json:"cwd,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	Size          int64     `json:"size,omitempty"`
	ContentSHA256 string    `json:"content_sha256,omitempty"`
}

type SessionIndex struct {
	Sessions []Session `json:"sessions"`
}

type DeletedIndex struct {
	Projects []DeletedRef `json:"projects,omitempty"`
	Sessions []DeletedRef `json:"sessions,omitempty"`
}

type DeletedRef struct {
	ID        string    `json:"id"`
	DeletedAt time.Time `json:"deleted_at"`
}

type RuntimeSession struct {
	ID              string    `json:"id,omitempty"`
	Runtime         string    `json:"runtime"`
	ProjectID       string    `json:"project_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	NativeSessionID string    `json:"native_session_id,omitempty"`
	ResumeMode      string    `json:"resume_mode,omitempty"`
	LastRunID       string    `json:"last_run_id,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type RuntimeSessionIndex struct {
	RuntimeSessions []RuntimeSession `json:"runtime_sessions"`
}

type WorkspaceRequest struct {
	RequestID     string `json:"request_id"`
	Operation     string `json:"operation"`
	ProjectID     string `json:"project_id"`
	Path          string `json:"path,omitempty"`
	Query         string `json:"query,omitempty"`
	Staged        bool   `json:"staged,omitempty"`
	Depth         int    `json:"depth,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	MaxBytes      int64  `json:"max_bytes,omitempty"`
	IncludeHidden bool   `json:"include_hidden,omitempty"`
}

type WorkspaceResponse struct {
	RequestID string                 `json:"request_id"`
	AgentID   string                 `json:"agent_id,omitempty"`
	Operation string                 `json:"operation"`
	ProjectID string                 `json:"project_id,omitempty"`
	Path      string                 `json:"path,omitempty"`
	Status    string                 `json:"status"`
	Code      string                 `json:"code,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Entries   []WorkspaceEntry       `json:"entries,omitempty"`
	File      *WorkspaceFile         `json:"file,omitempty"`
	Matches   []WorkspaceSearchMatch `json:"matches,omitempty"`
	Git       *WorkspaceGit          `json:"git,omitempty"`
	Diff      *WorkspaceGitDiff      `json:"diff,omitempty"`
	At        time.Time              `json:"at,omitempty"`
}

type WorkspaceGitUpdated struct {
	ProjectID string        `json:"project_id"`
	Status    string        `json:"status"`
	Code      string        `json:"code,omitempty"`
	Message   string        `json:"message,omitempty"`
	Git       *WorkspaceGit `json:"git,omitempty"`
	At        time.Time     `json:"at,omitempty"`
}

type WorkspaceEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	Size      int64     `json:"size,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	Sensitive bool      `json:"sensitive,omitempty"`
}

type WorkspaceFile struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Encoding  string    `json:"encoding"`
	Content   string    `json:"content,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Binary    bool      `json:"binary,omitempty"`
	Sensitive bool      `json:"sensitive,omitempty"`
}

type WorkspaceSearchMatch struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Score     int       `json:"score,omitempty"`
	Line      int       `json:"line,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	Sensitive bool      `json:"sensitive,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	Size      int64     `json:"size,omitempty"`
}

type WorkspaceGit struct {
	Branch    string               `json:"branch,omitempty"`
	Head      string               `json:"head,omitempty"`
	Upstream  string               `json:"upstream,omitempty"`
	Ahead     int                  `json:"ahead,omitempty"`
	Behind    int                  `json:"behind,omitempty"`
	Clean     bool                 `json:"clean"`
	Counts    WorkspaceGitCounts   `json:"counts"`
	Files     []WorkspaceGitChange `json:"files,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
}

type WorkspaceGitCounts struct {
	Modified   int `json:"modified,omitempty"`
	Added      int `json:"added,omitempty"`
	Deleted    int `json:"deleted,omitempty"`
	Renamed    int `json:"renamed,omitempty"`
	Untracked  int `json:"untracked,omitempty"`
	Conflicted int `json:"conflicted,omitempty"`
	Total      int `json:"total"`
}

type WorkspaceGitChange struct {
	Path       string `json:"path"`
	OldPath    string `json:"old_path,omitempty"`
	Index      string `json:"index,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Kind       string `json:"kind"`
	Conflicted bool   `json:"conflicted,omitempty"`
}

type WorkspaceGitDiff struct {
	Path      string `json:"path,omitempty"`
	Staged    bool   `json:"staged,omitempty"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content,omitempty"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

type HistoryChunk struct {
	SessionID string `json:"session_id"`
	Index     int    `json:"index"`
	Data      []byte `json:"data"`
	SHA256    string `json:"sha256"`
}

type HistoryMessage struct {
	SessionID string    `json:"session_id"`
	Index     int       `json:"index"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	At        time.Time `json:"at,omitempty"`
}

type HistoryMessages struct {
	SessionID string           `json:"session_id"`
	Messages  []HistoryMessage `json:"messages"`
}

type HistoryItem struct {
	SessionID     string          `json:"session_id"`
	Index         int             `json:"index"`
	Role          string          `json:"role,omitempty"`
	Kind          string          `json:"kind"`
	Text          string          `json:"text,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Source        string          `json:"source,omitempty"`
	SourceEventID string          `json:"source_event_id,omitempty"`
	At            time.Time       `json:"at,omitempty"`
}

type HistoryItems struct {
	SessionID  string        `json:"session_id"`
	Cursor     string        `json:"cursor,omitempty"`
	Reset      bool          `json:"reset,omitempty"`
	UploadID   string        `json:"upload_id,omitempty"`
	BatchIndex int           `json:"batch_index,omitempty"`
	BatchCount int           `json:"batch_count,omitempty"`
	Final      bool          `json:"final,omitempty"`
	Items      []HistoryItem `json:"items"`
}

type HistoryItemsAck struct {
	SessionID  string    `json:"session_id"`
	Cursor     string    `json:"cursor,omitempty"`
	UploadID   string    `json:"upload_id,omitempty"`
	BatchIndex int       `json:"batch_index,omitempty"`
	Status     string    `json:"status,omitempty"`
	Message    string    `json:"message,omitempty"`
	At         time.Time `json:"at,omitempty"`
}

type Command struct {
	CommandID      string          `json:"command_id"`
	RunID          string          `json:"run_id,omitempty"`
	Kind           string          `json:"kind"`
	ProjectID      string          `json:"project_id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	Mode           string          `json:"mode,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	DeadlineAt     time.Time       `json:"deadline_at,omitempty"`
	ExpiresAt      time.Time       `json:"expires_at,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

type RunEvent struct {
	EventID   string          `json:"event_id"`
	RunID     string          `json:"run_id"`
	CommandID string          `json:"command_id,omitempty"`
	ProjectID string          `json:"project_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       uint64          `json:"seq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	At        time.Time       `json:"at"`
}

func IsUsageKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "usage", "response.usage", "token_usage", "billing", "cost":
		return true
	default:
		return false
	}
}

type Ack struct {
	MessageID string    `json:"message_id,omitempty"`
	EventID   string    `json:"event_id,omitempty"`
	CommandID string    `json:"command_id,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	AckSeq    uint64    `json:"ack_seq,omitempty"`
	At        time.Time `json:"at"`
}

type ApprovalRequest struct {
	ID          string          `json:"id"`
	RunID       string          `json:"run_id,omitempty"`
	ProjectID   string          `json:"project_id,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	Category    string          `json:"category,omitempty"`
	Action      string          `json:"action,omitempty"`
	RiskLevel   string          `json:"risk_level,omitempty"`
	Description string          `json:"description,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	At          time.Time       `json:"at,omitempty"`
}

type ApprovalDecision struct {
	ApprovalID string    `json:"approval_id"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason,omitempty"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	DecidedAt  time.Time `json:"decided_at"`
}

type Heartbeat struct {
	AgentID      string    `json:"agent_id"`
	DeviceID     string    `json:"device_id"`
	Status       string    `json:"status"`
	RunningRuns  []string  `json:"running_runs"`
	LastEventSeq uint64    `json:"last_event_seq"`
	At           time.Time `json:"at"`
	Health       Health    `json:"health,omitempty"`
}

type Health struct {
	Status             string    `json:"status"`
	CredentialOK       bool      `json:"credential_ok"`
	CredentialSource   string    `json:"credential_source,omitempty"`
	BaseURLFingerprint string    `json:"base_url_fingerprint,omitempty"`
	KeyFingerprint     string    `json:"key_fingerprint,omitempty"`
	Model              string    `json:"model,omitempty"`
	CodexRoots         int       `json:"codex_roots"`
	CodexRootsMissing  int       `json:"codex_roots_missing"`
	LastScanAt         time.Time `json:"last_scan_at,omitempty"`
	LastScanError      string    `json:"last_scan_error,omitempty"`
	LastRuntimeError   string    `json:"last_runtime_error,omitempty"`
}

type HistoryChunkAck struct {
	SessionID string    `json:"session_id"`
	Index     int       `json:"index"`
	SHA256    string    `json:"sha256,omitempty"`
	Status    string    `json:"status,omitempty"`
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at,omitempty"`
}

type SyncCursor struct {
	Stream    string    `json:"stream"`
	SubjectID string    `json:"subject_id,omitempty"`
	Cursor    string    `json:"cursor"`
	At        time.Time `json:"at,omitempty"`
}

func Raw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}

func Decode[T any](raw json.RawMessage) (T, error) {
	var value T
	err := json.Unmarshal(raw, &value)
	return value, err
}

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

const Version = "2026-06-17"

type Envelope struct {
	Version   string          `json:"version"`
	MessageID string          `json:"message_id"`
	Type      string          `json:"type"`
	AgentID   string          `json:"agent_id,omitempty"`
	DeviceID  string          `json:"device_id,omitempty"`
	Seq       uint64          `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Nonce     string          `json:"nonce"`
	KeyID     string          `json:"key_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Signature string          `json:"signature,omitempty"`
}

type AgentRegister struct {
	AgentID      string       `json:"agent_id"`
	WorkspaceID  string       `json:"workspace_id"`
	DeviceID     string       `json:"device_id"`
	Device       DeviceInfo   `json:"device"`
	Credential   Credential   `json:"credential"`
	Capabilities Capabilities `json:"capabilities"`
}

type DeviceInfo struct {
	InstallID    string `json:"install_id"`
	MachineHash  string `json:"machine_hash"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	UsernameHash string `json:"username_hash,omitempty"`
	AgentVersion string `json:"agent_version"`
}

type Credential struct {
	BaseURLFingerprint string `json:"base_url_fingerprint"`
	KeyFingerprint     string `json:"key_fingerprint"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Source             string `json:"source,omitempty"`
}

type Capabilities struct {
	SupportsSDK       bool     `json:"supports_sdk"`
	SupportsHistory   bool     `json:"supports_history"`
	SupportsTerminal  bool     `json:"supports_terminal"`
	SupportsGit       bool     `json:"supports_git"`
	Features          []string `json:"features,omitempty"`
	MaxConcurrentRuns int      `json:"max_concurrent_runs"`
}

type RootIndex struct {
	Roots []Root `json:"roots"`
}

type Root struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Exists bool   `json:"exists"`
}

type ProjectIndex struct {
	Projects []Project `json:"projects"`
}

type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	LastActiveAt time.Time `json:"last_active_at,omitempty"`
	HasAgentsMD  bool      `json:"has_agents_md"`
	GitBranch    string    `json:"git_branch,omitempty"`
}

type SessionIndex struct {
	Sessions []Session `json:"sessions"`
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

type RuntimeSession struct {
	ID              string    `json:"id,omitempty"`
	Runtime         string    `json:"runtime"`
	ProjectID       string    `json:"project_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	NativeSessionID string    `json:"native_session_id,omitempty"`
	LastResponseID  string    `json:"last_response_id,omitempty"`
	ResumeMode      string    `json:"resume_mode,omitempty"`
	LastRunID       string    `json:"last_run_id,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
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

type Command struct {
	CommandID string          `json:"command_id"`
	RunID     string          `json:"run_id,omitempty"`
	Kind      string          `json:"kind"`
	ProjectID string          `json:"project_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
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

type Ack struct {
	MessageID string    `json:"message_id,omitempty"`
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
}

func NewEnvelope(messageType, agentID, deviceID string, seq uint64, data any) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		Version:   Version,
		MessageID: NewID("msg"),
		Type:      messageType,
		AgentID:   agentID,
		DeviceID:  deviceID,
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Nonce:     NewID("nonce"),
		Data:      raw,
	}, nil
}

func Decode[T any](raw json.RawMessage) (T, error) {
	var value T
	err := json.Unmarshal(raw, &value)
	return value, err
}

func Raw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

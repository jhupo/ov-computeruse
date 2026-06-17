package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/config"
	"ov-computeruse/agent/internal/device"
	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/runs"
	"ov-computeruse/agent/internal/securestore"
	"ov-computeruse/agent/internal/security"
)

type Client struct {
	identity      securestore.Identity
	manager       *runs.Manager
	scanner       codexscan.Scanner
	device        device.Info
	cfg           config.Config
	state         *localstate.Store
	noScan        bool
	uploadHistory bool
	startedAt     time.Time
	lastScanAt    time.Time
	lastScanErr   string
	logger        *slog.Logger
	dialer        Dialer

	mu   sync.Mutex
	conn Conn
	seq  uint64
}

func NewClient(identity securestore.Identity, manager *runs.Manager, scanner codexscan.Scanner, deviceInfo device.Info, cfg config.Config, state *localstate.Store, noScan bool, uploadHistory bool, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		identity:      identity,
		manager:       manager,
		scanner:       scanner,
		device:        deviceInfo,
		cfg:           cfg,
		state:         state,
		noScan:        noScan,
		uploadHistory: uploadHistory,
		startedAt:     time.Now().UTC(),
		logger:        logger,
		dialer:        WebSocketDialer{},
	}
}

func (c *Client) Run(ctx context.Context) error {
	if c.manager != nil {
		c.manager.SetSink(c)
	}
	delay := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		endpointURL, err := websocketURL(c.identity.ServerURL)
		if err != nil {
			return err
		}
		conn, err := c.dialer.Dial(ctx, Endpoint{
			URL:   endpointURL,
			Token: c.identity.AgentSecret,
		})
		if err != nil {
			c.logger.Warn("connect failed", "error", err)
			if waitErr := sleep(ctx, delay); waitErr != nil {
				return waitErr
			}
			delay = nextDelay(delay, 30*time.Second)
			continue
		}
		c.setConn(conn)
		delay = time.Second
		err = c.serve(ctx, conn)
		_ = conn.Close()
		c.clearConn(conn)
		if errors.Is(err, context.Canceled) {
			return err
		}
		c.logger.Warn("connection closed", "error", err)
	}
}

func (c *Client) Emit(ctx context.Context, event protocol.RunEvent) error {
	return c.send(ctx, "run.event", event)
}

func (c *Client) serve(ctx context.Context, conn Conn) error {
	if err := c.register(ctx); err != nil {
		return err
	}
	if err := c.uploadIndex(ctx); err != nil {
		return err
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 2)
	go func() { errCh <- c.heartbeatLoop(connCtx) }()
	go func() { errCh <- c.readLoop(connCtx, conn) }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (c *Client) register(ctx context.Context) error {
	cred, _ := c.scanner.Credential()
	register := protocol.AgentRegister{
		AgentID:     c.identity.AgentID,
		WorkspaceID: c.identity.WorkspaceID,
		DeviceID:    c.identity.DeviceID,
		Device: protocol.DeviceInfo{
			InstallID:    c.device.InstallID,
			MachineHash:  c.device.MachineHash,
			Hostname:     c.device.Hostname,
			OS:           c.device.OS,
			Arch:         c.device.Arch,
			UsernameHash: c.device.UsernameHash,
			AgentVersion: c.device.AgentVersion,
			InstallState: c.installState(""),
		},
		Credential: protocol.Credential{
			BaseURLFingerprint: security.FingerprintSecret(strings.TrimRight(strings.ToLower(cred.BaseURL), "/")),
			KeyFingerprint:     cred.Fingerprint,
			Provider:           cred.Provider,
			Model:              cred.Model,
			Source:             cred.Source,
		},
		Capabilities: protocol.Capabilities{
			SupportsSDK:       true,
			SupportsHistory:   true,
			SupportsTerminal:  false,
			SupportsGit:       false,
			Features:          []string{"codex.scan", "history.items", "run.events", "runtime.session", "approval.decision", "command.new_session", "command.resume", "command.send", "command.stop", "command.refresh_index"},
			MaxConcurrentRuns: 1,
		},
	}
	if c.uploadHistory {
		register.Capabilities.Features = append(register.Capabilities.Features, "history.upload")
	}
	return c.send(ctx, "agent.register", register)
}

func (c *Client) uploadIndex(ctx context.Context) error {
	if c.noScan {
		return c.send(ctx, "index.updated", map[string]any{
			"disabled": true,
			"at":       time.Now().UTC(),
		})
	}
	result, err := c.scanner.Scan(ctx)
	if err != nil {
		c.recordScan(time.Time{}, err)
		return err
	}
	c.recordScan(time.Now().UTC(), nil)
	var deleted localstate.DeletedIndex
	if c.state != nil {
		var err error
		deleted, err = c.state.SaveScanResult(ctx, result)
		if err != nil {
			return err
		}
	}
	roots := make([]protocol.Root, 0, len(result.Roots))
	for _, root := range result.Roots {
		roots = append(roots, protocol.Root{
			Path:   root.Path,
			Kind:   root.Kind,
			Source: root.Source,
			Exists: root.Exists,
		})
	}
	if err := c.send(ctx, "index.roots", protocol.RootIndex{Roots: roots}); err != nil {
		return err
	}
	projects := make([]protocol.Project, 0, len(result.Projects))
	for _, project := range result.Projects {
		projects = append(projects, protocol.Project{
			ID:           project.ID,
			Name:         project.Name,
			Path:         project.Path,
			LastActiveAt: project.LastActiveAt,
			HasAgentsMD:  project.HasAgentsMD,
			GitBranch:    project.GitBranch,
		})
	}
	if err := c.send(ctx, "index.projects", protocol.ProjectIndex{Projects: projects}); err != nil {
		return err
	}
	sessions := make([]protocol.Session, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		sessions = append(sessions, protocol.Session{
			ID:            session.ID,
			IDSource:      session.IDSource,
			ProjectID:     session.ProjectID,
			Title:         session.Title,
			Path:          session.Path,
			CWD:           session.CWD,
			UpdatedAt:     session.UpdatedAt,
			Size:          session.Size,
			ContentSHA256: session.ContentSHA256,
		})
	}
	if err := c.send(ctx, "index.sessions", protocol.SessionIndex{Sessions: sessions}); err != nil {
		return err
	}
	if len(deleted.Projects) > 0 || len(deleted.Sessions) > 0 {
		if err := c.send(ctx, "index.deleted", protocol.DeletedIndex{
			Projects: protocolDeletedRefs(deleted.Projects),
			Sessions: protocolDeletedRefs(deleted.Sessions),
		}); err != nil {
			return err
		}
	}
	for _, session := range result.Sessions {
		if err := c.uploadHistoryItems(ctx, session); err != nil {
			c.logger.Warn("history items upload skipped", "session_id", session.ID, "error", err)
		}
	}
	if !c.uploadHistory {
		return c.send(ctx, "index.updated", map[string]any{
			"roots":          len(roots),
			"projects":       len(projects),
			"sessions":       len(sessions),
			"history_upload": false,
			"at":             time.Now().UTC(),
		})
	}
	for _, session := range result.Sessions {
		err := c.scanner.ForEachHistoryChunk(ctx, session, 64<<10, func(chunk codexscan.HistoryChunk) error {
			if c.state != nil {
				if err := c.state.SaveHistoryChunk(ctx, chunk); err != nil {
					return err
				}
				acked, err := c.state.IsHistoryChunkAcked(ctx, chunk.SessionID, chunk.Index, chunk.SHA256)
				if err != nil {
					return err
				}
				if acked {
					return nil
				}
			}
			if err := c.send(ctx, "history.chunk", protocol.HistoryChunk{
				SessionID: chunk.SessionID,
				Index:     chunk.Index,
				Data:      chunk.Data,
				SHA256:    chunk.SHA256,
			}); err != nil {
				if c.state != nil {
					_ = c.state.MarkHistoryChunkError(ctx, chunk.SessionID, chunk.Index, chunk.SHA256, err)
				}
				return err
			}
			if c.state != nil {
				if err := c.state.MarkHistoryChunkSent(ctx, chunk.SessionID, chunk.Index, chunk.SHA256); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			c.logger.Warn("history upload skipped", "session_id", session.ID, "error", err)
		}
	}
	return c.send(ctx, "index.updated", map[string]any{
		"roots":    len(roots),
		"projects": len(projects),
		"sessions": len(sessions),
		"at":       time.Now().UTC(),
	})
}

func protocolDeletedRefs(items []localstate.DeletedRef) []protocol.DeletedRef {
	out := make([]protocol.DeletedRef, 0, len(items))
	for _, item := range items {
		out = append(out, protocol.DeletedRef{ID: item.ID, DeletedAt: item.DeletedAt})
	}
	return out
}

func (c *Client) uploadHistoryItems(ctx context.Context, session codexscan.Session) error {
	cursor := historyCursor(session)
	if c.state != nil {
		existing, err := c.state.SyncCursor(ctx, "history.items", session.ID)
		if err == nil && existing.Cursor == cursor {
			return nil
		}
	}
	const historyItemBatchSize = 200
	const historyItemBatchBytes = 1 << 20
	reset := true
	sent := 0
	batchBytes := 0
	out := make([]protocol.HistoryItem, 0, historyItemBatchSize)
	flush := func() error {
		if len(out) == 0 {
			return nil
		}
		if err := c.send(ctx, "history.items", protocol.HistoryItems{SessionID: session.ID, Cursor: cursor, Reset: reset, Items: out}); err != nil {
			return err
		}
		reset = false
		sent += len(out)
		out = make([]protocol.HistoryItem, 0, historyItemBatchSize)
		batchBytes = 0
		return nil
	}
	err := codexscan.ForEachSessionItem(ctx, session, 256<<10, func(item codexscan.HistoryItem) error {
		wire := protocol.HistoryItem{
			SessionID:     session.ID,
			Index:         item.Index,
			Role:          item.Role,
			Kind:          item.Kind,
			Text:          item.Text,
			Payload:       item.Payload,
			Source:        "codex.history",
			SourceEventID: item.SourceEventID,
			At:            item.At,
		}
		out = append(out, wire)
		batchBytes += len(wire.Text) + len(wire.Payload)
		if len(out) >= historyItemBatchSize || batchBytes >= historyItemBatchBytes {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if sent == 0 {
		if err := c.send(ctx, "history.items", protocol.HistoryItems{SessionID: session.ID, Cursor: cursor, Reset: true}); err != nil {
			return err
		}
	}
	if c.state != nil {
		if err := c.state.SaveSyncCursor(ctx, localstate.SyncCursor{Stream: "history.items", SubjectID: session.ID, Cursor: cursor}); err != nil {
			return err
		}
	}
	return c.send(ctx, "sync.cursor", protocol.SyncCursor{Stream: "history.items", SubjectID: session.ID, Cursor: cursor, At: time.Now().UTC()})
}

func historyCursor(session codexscan.Session) string {
	return string(protocol.Raw(map[string]any{
		"sha256":     session.ContentSHA256,
		"size":       session.Size,
		"updated_at": session.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}))
}

func (c *Client) heartbeatLoop(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := c.heartbeat(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) heartbeat(ctx context.Context) error {
	running := []string(nil)
	lastSeq := uint64(0)
	status := "online"
	if c.manager != nil {
		running = c.manager.RunningRuns()
		lastSeq = c.manager.LastEventSeq()
		status = string(c.manager.State())
		if status == "idle" {
			status = "online"
		}
	}
	return c.send(ctx, "agent.heartbeat", protocol.Heartbeat{
		AgentID:      c.identity.AgentID,
		DeviceID:     c.identity.DeviceID,
		Status:       status,
		RunningRuns:  running,
		LastEventSeq: lastSeq,
		At:           time.Now().UTC(),
		Health:       c.health(ctx),
	})
}

func (c *Client) installState(lastError string) protocol.InstallState {
	return protocol.InstallState{
		Installed:          c.identity.AgentID != "",
		ServiceRegistered:  envBool("OV_AGENT_SERVICE_REGISTERED"),
		ServiceRunning:     envBool("OV_AGENT_SERVICE_RUNNING"),
		AutostartEnabled:   envBool("OV_AGENT_AUTOSTART_ENABLED"),
		PackageType:        firstNonEmpty(os.Getenv("OV_AGENT_PACKAGE_TYPE"), packageType()),
		Channel:            os.Getenv("OV_AGENT_CHANNEL"),
		ConfigDir:          c.cfg.ConfigDir,
		DataDir:            c.cfg.DataDir,
		StatePath:          c.cfg.StatePath,
		StateDBPath:        c.cfg.StateDBPath,
		LogDir:             c.cfg.LogDir,
		CodexHome:          c.cfg.CodexHome,
		LastStartAt:        c.startedAt,
		LastInstallCheckAt: time.Now().UTC(),
		LastError:          lastError,
	}
}

func (c *Client) health(ctx context.Context) protocol.Health {
	health := protocol.Health{Status: "ok"}
	cred, err := c.scanner.Credential()
	if err != nil {
		health.Status = "degraded"
		health.LastRuntimeError = err.Error()
	} else {
		health.CredentialOK = true
		health.CredentialSource = cred.Source
		health.BaseURLFingerprint = security.FingerprintSecret(strings.TrimRight(strings.ToLower(cred.BaseURL), "/"))
		health.KeyFingerprint = cred.Fingerprint
		health.Model = cred.Model
	}
	for _, root := range c.scanner.DiscoverRoots() {
		health.CodexRoots++
		if !root.Exists {
			health.CodexRootsMissing++
		}
	}
	if c.noScan {
		health.Status = "scan_disabled"
		return health
	}
	c.mu.Lock()
	lastScanAt := c.lastScanAt
	lastScanErr := c.lastScanErr
	c.mu.Unlock()
	health.LastScanAt = lastScanAt
	if lastScanErr != "" {
		health.Status = "degraded"
		health.LastScanError = lastScanErr
	}
	return health
}

func (c *Client) recordScan(at time.Time, scanErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !at.IsZero() {
		c.lastScanAt = at
	}
	if scanErr != nil {
		c.lastScanErr = scanErr.Error()
	} else {
		c.lastScanErr = ""
	}
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func packageType() string {
	switch runtime.GOOS {
	case "windows":
		return "inno"
	case "darwin":
		return "pkg"
	case "linux":
		return "deb_rpm"
	default:
		return runtime.GOOS
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (c *Client) readLoop(ctx context.Context, conn Conn) error {
	replay := protocol.NewReplayGuard(10 * time.Minute)
	for {
		env, err := conn.ReadEnvelope(ctx)
		if err != nil {
			return err
		}
		if env.Signature == "" || !security.Verify(c.identity.AgentSecret, unsignedBytes(env), env.Signature) {
			_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, Status: "rejected", Message: "invalid signature", At: time.Now().UTC()})
			continue
		}
		decrypted, err := protocol.DecryptEnvelopeData(c.identity.AgentSecret, env)
		if err != nil {
			_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, Status: "rejected", Message: "invalid encryption", At: time.Now().UTC()})
			continue
		}
		env = decrypted
		if err := replay.Accept(env, time.Now().UTC()); err != nil {
			_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, Status: "rejected", Message: err.Error(), At: time.Now().UTC()})
			continue
		}
		switch env.Type {
		case "command":
			command, err := protocol.Decode[protocol.Command](env.Data)
			if err != nil {
				_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, Status: "rejected", Message: err.Error(), At: time.Now().UTC()})
				continue
			}
			if strings.TrimPrefix(command.Kind, "command.") == "refresh_index" {
				ack := protocol.Ack{MessageID: env.MessageID, CommandID: command.CommandID, Status: "ok", Message: "refresh started", At: time.Now().UTC()}
				_ = c.send(ctx, "ack", ack)
				if err := c.uploadIndex(ctx); err != nil {
					_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, CommandID: command.CommandID, Status: "failed", Message: err.Error(), At: time.Now().UTC()})
				}
				continue
			}
			ack := c.manager.Handle(ctx, command)
			ack.MessageID = env.MessageID
			_ = c.send(ctx, "ack", ack)
		case "command.refresh_index":
			_ = c.uploadIndex(ctx)
		case "approval.decision":
			decision, err := protocol.Decode[protocol.ApprovalDecision](env.Data)
			if err != nil {
				_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, Status: "rejected", Message: err.Error(), At: time.Now().UTC()})
				continue
			}
			ack := c.manager.DecideApproval(ctx, decision)
			ack.MessageID = env.MessageID
			_ = c.send(ctx, "ack", ack)
		case "history.chunk.ack":
			if c.state == nil {
				continue
			}
			ack, err := protocol.Decode[protocol.HistoryChunkAck](env.Data)
			if err != nil {
				continue
			}
			if ack.Status == "" || ack.Status == "ok" || ack.Status == "acked" {
				_ = c.state.MarkHistoryChunkAcked(ctx, localstate.HistoryChunkAck{
					SessionID: ack.SessionID,
					Index:     ack.Index,
					SHA256:    ack.SHA256,
				})
			}
		case "sync.cursor":
			if c.state == nil {
				continue
			}
			cursor, err := protocol.Decode[protocol.SyncCursor](env.Data)
			if err != nil {
				continue
			}
			_ = c.state.SaveSyncCursor(ctx, localstate.SyncCursor{
				Stream:    cursor.Stream,
				SubjectID: cursor.SubjectID,
				Cursor:    cursor.Cursor,
			})
		}
	}
}

func (c *Client) send(ctx context.Context, messageType string, data any) error {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("not connected")
	}
	env, err := protocol.NewEnvelope(messageType, c.identity.AgentID, c.identity.DeviceID, seq, data)
	if err != nil {
		return err
	}
	env, err = protocol.EncryptEnvelopeData(c.identity.AgentSecret, env)
	if err != nil {
		return err
	}
	env.Signature = security.Sign(c.identity.AgentSecret, unsignedBytes(env))
	return conn.WriteEnvelope(ctx, env)
}

func (c *Client) setConn(conn Conn) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

func (c *Client) clearConn(conn Conn) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	c.mu.Unlock()
}

func websocketURL(serverURL string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("server url must use https")
	}
	parsed.Scheme = "wss"
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws/agent"
	return parsed.String(), nil
}

func unsignedBytes(env protocol.Envelope) []byte {
	env.Signature = ""
	return protocol.Raw(env)
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextDelay(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}

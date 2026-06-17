package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"ov-computeruse/agent/internal/codexscan"
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
	state         *localstate.Store
	noScan        bool
	uploadHistory bool
	logger        *slog.Logger
	dialer        Dialer

	mu   sync.Mutex
	conn Conn
	seq  uint64
}

func NewClient(identity securestore.Identity, manager *runs.Manager, scanner codexscan.Scanner, deviceInfo device.Info, state *localstate.Store, noScan bool, uploadHistory bool, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		identity:      identity,
		manager:       manager,
		scanner:       scanner,
		device:        deviceInfo,
		state:         state,
		noScan:        noScan,
		uploadHistory: uploadHistory,
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
			Features:          []string{"codex.scan", "run.events", "runtime.session", "command.new_session", "command.resume", "command.send", "command.stop", "command.refresh_index"},
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
		return err
	}
	if c.state != nil {
		if err := c.state.SaveScanResult(ctx, result); err != nil {
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
	for _, session := range result.Sessions {
		if err := c.uploadHistoryMessages(ctx, session); err != nil {
			c.logger.Warn("history messages upload skipped", "session_id", session.ID, "error", err)
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

func (c *Client) uploadHistoryMessages(ctx context.Context, session codexscan.Session) error {
	messages, err := codexscan.ReadSessionMessages(ctx, session.Path, 200, 256<<10)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	out := make([]protocol.HistoryMessage, 0, len(messages))
	for idx, message := range messages {
		out = append(out, protocol.HistoryMessage{
			SessionID: session.ID,
			Index:     idx,
			Role:      message.Role,
			Text:      message.Text,
			At:        message.At,
		})
	}
	return c.send(ctx, "history.messages", protocol.HistoryMessages{SessionID: session.ID, Messages: out})
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
	})
}

func (c *Client) readLoop(ctx context.Context, conn Conn) error {
	for {
		env, err := conn.ReadEnvelope(ctx)
		if err != nil {
			return err
		}
		if env.Signature == "" || !security.Verify(c.identity.AgentSecret, unsignedBytes(env), env.Signature) {
			_ = c.send(ctx, "ack", protocol.Ack{MessageID: env.MessageID, Status: "rejected", Message: "invalid signature", At: time.Now().UTC()})
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

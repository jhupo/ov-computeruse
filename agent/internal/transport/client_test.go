package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/config"
	"ov-computeruse/agent/internal/device"
	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/securestore"
	"ov-computeruse/agent/internal/security"
)

func TestUniqueRuntimeSessionsKeepsNativeOnlySessions(t *testing.T) {
	oldAt := time.Now().UTC().Add(-time.Minute)
	newAt := time.Now().UTC()
	sessions := uniqueRuntimeSessions([]protocol.RuntimeSession{
		{Runtime: protocol.RuntimeCodexCLI, NativeSessionID: "native_1", UpdatedAt: oldAt},
		{Runtime: protocol.RuntimeCodexCLI, NativeSessionID: "native_1", UpdatedAt: newAt},
		{Runtime: protocol.RuntimeCodexCLI, SessionID: "session_1", NativeSessionID: "native_2", UpdatedAt: newAt},
	})
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2: %+v", len(sessions), sessions)
	}
	foundNative := false
	for _, session := range sessions {
		if session.NativeSessionID == "native_1" {
			foundNative = true
			if !session.UpdatedAt.Equal(newAt) {
				t.Fatalf("native session = %+v", session)
			}
		}
	}
	if !foundNative {
		t.Fatalf("native-only runtime session was dropped: %+v", sessions)
	}
}

func TestShouldRefreshIndexAfterTerminalRunEvents(t *testing.T) {
	client := &Client{}
	for _, kind := range []string{"run.done", "run.completed", "run.error", "run.failed", "run.stopped"} {
		if !client.shouldRefreshIndexAfter(protocol.RunEvent{Kind: kind}) {
			t.Fatalf("expected refresh after %s", kind)
		}
	}
	if client.shouldRefreshIndexAfter(protocol.RunEvent{Kind: "run.status"}) {
		t.Fatal("did not expect refresh after run.status")
	}
	client.noScan = true
	if client.shouldRefreshIndexAfter(protocol.RunEvent{Kind: "run.done"}) {
		t.Fatal("did not expect refresh when scan is disabled")
	}
}

func TestEmitSendsRunEventImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn := newMemoryConn()
	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		blockingScanner{},
		protocolDevice(),
		defaultConfig(),
		nil,
		false,
		false,
		nil,
	)
	client.setConn(conn)

	event := protocol.RunEvent{
		EventID: "evt_1",
		RunID:   "run_1",
		Seq:     7,
		Kind:    "assistant.message.done",
		Payload: protocol.Raw(map[string]string{"text": "stream-now"}),
		At:      time.Now().UTC(),
	}
	if err := client.Emit(ctx, event); err != nil {
		t.Fatalf("emit: %v", err)
	}

	env := waitForWrittenEnvelope(t, conn, "run.event")
	decrypted, err := protocol.DecryptEnvelopeData("secret", env)
	if err != nil {
		t.Fatalf("decrypt envelope: %v", err)
	}
	var sent protocol.RunEvent
	if err := json.Unmarshal(decrypted.Data, &sent); err != nil {
		t.Fatalf("decode run event: %v", err)
	}
	if sent.EventID != event.EventID || sent.RunID != event.RunID || sent.Seq != event.Seq || sent.Kind != event.Kind {
		t.Fatalf("sent event = %+v, want %+v", sent, event)
	}
	if string(sent.Payload) != string(event.Payload) {
		t.Fatalf("payload = %s, want %s", sent.Payload, event.Payload)
	}
}

func TestCapabilityFeaturesDoNotAdvertiseApprovalDecision(t *testing.T) {
	client := &Client{}
	features := client.capabilityFeatures(protocol.RuntimeCodexCLI)
	if !hasFeature(features, "command.new_session") || !hasFeature(features, "runtime."+protocol.RuntimeCodexCLI) {
		t.Fatalf("runtime features missing: %#v", features)
	}
	if hasFeature(features, "approval.decision") {
		t.Fatalf("approval.decision must not be advertised until codex cli approval protocol is wired: %#v", features)
	}
}

func TestCapabilityFeaturesAdvertiseLocalShellWithoutApprovalDecision(t *testing.T) {
	client := &Client{cfg: config.Config{AllowLocalShell: true}}
	features := client.capabilityFeatures(protocol.RuntimeCodexCLI)
	if !hasFeature(features, "tool.local_shell") || !hasFeature(features, "terminal.output") {
		t.Fatalf("local shell features missing: %#v", features)
	}
	if hasFeature(features, "approval.decision") {
		t.Fatalf("approval.decision must not be inferred from local shell policy: %#v", features)
	}
}

func TestFindHistorySessionMatchesNativeRuntimeSession(t *testing.T) {
	result := codexscan.Result{
		Sessions: []codexscan.Session{
			{ID: "indexed_session", Path: "session.jsonl"},
		},
		RuntimeSessions: []codexscan.RuntimeSession{
			{Runtime: protocol.RuntimeCodexCLI, SessionID: "indexed_session", NativeSessionID: "native_thread"},
		},
	}
	session, ok := findHistorySession(result, protocol.RuntimeSession{Runtime: protocol.RuntimeCodexCLI, NativeSessionID: "native_thread"})
	if !ok {
		t.Fatal("expected native runtime session to resolve to indexed history session")
	}
	if session.ID != "indexed_session" {
		t.Fatalf("session id = %q, want indexed_session", session.ID)
	}
}

func TestScanRunHistorySessionRetriesUntilHistoryAppears(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		&sequenceScanner{results: []codexscan.Result{
			{},
			{
				Sessions: []codexscan.Session{{ID: "history_session"}},
				RuntimeSessions: []codexscan.RuntimeSession{{
					Runtime:         protocol.RuntimeCodexCLI,
					SessionID:       "history_session",
					NativeSessionID: "native_thread",
				}},
			},
		}},
		protocolDevice(),
		defaultConfig(),
		nil,
		false,
		false,
		nil,
	)
	_, session, err := client.scanRunHistorySession(ctx, protocol.RuntimeSession{Runtime: protocol.RuntimeCodexCLI, NativeSessionID: "native_thread"})
	if err != nil {
		t.Fatalf("scan history session: %v", err)
	}
	if session.ID != "history_session" {
		t.Fatalf("session id = %q, want history_session", session.ID)
	}
}

func TestServeStartsReadLoopBeforeIndexUploadCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn := newMemoryConn()
	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		blockingScanner{},
		protocolDevice(),
		defaultConfig(),
		nil,
		false,
		false,
		nil,
	)
	client.setConn(conn)

	done := make(chan error, 1)
	go func() { done <- client.serve(ctx, conn) }()

	waitForWrittenType(t, conn, "agent.register")
	if err := conn.pushEncrypted("command", "server", "server_device", 1, protocol.Command{CommandID: "cmd_1", Kind: "command.approval_decision"}, "secret"); err != nil {
		t.Fatal(err)
	}
	waitForWrittenType(t, conn, "ack")
	conn.closeWith(errors.New("done"))

	select {
	case err := <-done:
		if err == nil || err.Error() != "done" {
			t.Fatalf("serve error = %v, want done", err)
		}
	case <-ctx.Done():
		t.Fatal("serve did not start read loop while index upload was running")
	}
}

func TestRefreshIndexCommandAckIsPersistedAndReplayed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	state, err := localstate.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	conn := newMemoryConn()
	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		blockingScanner{},
		protocolDevice(),
		defaultConfig(),
		state,
		true,
		false,
		nil,
	)
	client.setConn(conn)

	done := make(chan error, 1)
	go func() { done <- client.readLoop(ctx, conn) }()

	command := protocol.Command{CommandID: "cmd_refresh_1", Kind: "command.refresh_index"}
	if err := conn.pushEncrypted("command", "server", "server_device", 1, command, "secret"); err != nil {
		t.Fatal(err)
	}
	waitForWrittenType(t, conn, "index.updated")
	firstAck := waitForWrittenAck(t, conn)
	if firstAck.CommandID != command.CommandID || firstAck.Status != "ok" {
		t.Fatalf("first ack = %+v", firstAck)
	}

	if err := conn.pushEncrypted("command", "server", "server_device", 2, command, "secret"); err != nil {
		t.Fatal(err)
	}
	secondAck := waitForWrittenAck(t, conn)
	if secondAck.CommandID != command.CommandID || secondAck.Status != firstAck.Status || secondAck.Message != firstAck.Message {
		t.Fatalf("second ack = %+v, want replay of %+v", secondAck, firstAck)
	}
	assertNoWrittenType(t, conn, "index.updated", 150*time.Millisecond)

	stored, ok, err := state.CommandAck(ctx, command.CommandID)
	if err != nil {
		t.Fatalf("load stored ack: %v", err)
	}
	if !ok || stored.Status != "ok" {
		t.Fatalf("stored ack = %+v, ok=%v", stored, ok)
	}

	conn.closeWith(errors.New("done"))
	select {
	case err := <-done:
		if err == nil || err.Error() != "done" {
			t.Fatalf("read loop error = %v, want done", err)
		}
	case <-ctx.Done():
		t.Fatal("read loop did not exit")
	}
}

func TestWorkspaceRequestRefreshesIndexWithoutHistoryUpload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	conn := newMemoryConn()
	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		staticScanner{result: codexscan.Result{
			Projects: []codexscan.Project{{ID: "project_1", Name: "repo", Path: root, LastActiveAt: time.Now().UTC()}},
			Sessions: []codexscan.Session{{ID: "session_1", ProjectID: "project_1", Path: filepath.Join(root, "history.jsonl"), CWD: root, UpdatedAt: time.Now().UTC()}},
		}},
		protocolDevice(),
		defaultConfig(),
		state,
		false,
		true,
		nil,
	)
	client.setConn(conn)

	request := protocol.WorkspaceRequest{RequestID: "wsreq_1", Operation: "read", ProjectID: "project_1", Path: "main.go"}
	go client.handleWorkspaceRequest(ctx, request)

	waitForWrittenType(t, conn, "index.roots")
	waitForWrittenType(t, conn, "index.projects")
	waitForWrittenType(t, conn, "index.sessions")
	indexUpdated := decryptEnvelopeData[map[string]any](t, waitForWrittenEnvelope(t, conn, "index.updated"))
	if indexUpdated["source"] != "workspace.request" || indexUpdated["history_upload"] != false {
		t.Fatalf("index updated payload = %#v", indexUpdated)
	}
	resp := decryptEnvelopeData[protocol.WorkspaceResponse](t, waitForWrittenEnvelope(t, conn, "workspace.response"))
	if resp.Status != "ok" || resp.File == nil || resp.File.Content != "package main\n" {
		t.Fatalf("workspace response = %+v", resp)
	}
	assertNoWrittenType(t, conn, "history.items", 150*time.Millisecond)
	assertNoWrittenType(t, conn, "history.chunk", 150*time.Millisecond)
}

func TestApprovalDecisionCommandAckIsPersistedAndReplayed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	state, err := localstate.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	conn := newMemoryConn()
	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		blockingScanner{},
		protocolDevice(),
		defaultConfig(),
		state,
		true,
		false,
		nil,
	)
	client.setConn(conn)

	done := make(chan error, 1)
	go func() { done <- client.readLoop(ctx, conn) }()

	command := protocol.Command{
		CommandID: "cmd_approval_1",
		RunID:     "run_1",
		Kind:      "command.approval_decision",
		Payload:   protocol.Raw(protocol.ApprovalDecision{ApprovalID: "approval_1", Decision: "approved"}),
	}
	if err := conn.pushEncrypted("command", "server", "server_device", 1, command, "secret"); err != nil {
		t.Fatal(err)
	}
	firstAck := waitForWrittenAck(t, conn)
	if firstAck.CommandID != command.CommandID || firstAck.RunID != command.RunID || firstAck.Status != "rejected" {
		t.Fatalf("first ack = %+v", firstAck)
	}

	if err := conn.pushEncrypted("command", "server", "server_device", 2, command, "secret"); err != nil {
		t.Fatal(err)
	}
	secondAck := waitForWrittenAck(t, conn)
	if secondAck.CommandID != firstAck.CommandID || secondAck.RunID != firstAck.RunID || secondAck.Status != firstAck.Status || secondAck.Message != firstAck.Message {
		t.Fatalf("second ack = %+v, want replay of %+v", secondAck, firstAck)
	}

	stored, ok, err := state.CommandAck(ctx, command.CommandID)
	if err != nil {
		t.Fatalf("load stored ack: %v", err)
	}
	if !ok || stored.Status != "rejected" {
		t.Fatalf("stored ack = %+v, ok=%v", stored, ok)
	}

	conn.closeWith(errors.New("done"))
	select {
	case err := <-done:
		if err == nil || err.Error() != "done" {
			t.Fatalf("read loop error = %v, want done", err)
		}
	case <-ctx.Done():
		t.Fatal("read loop did not exit")
	}
}

func TestOutboxTerminalRunEventTriggersHistorySync(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	content := "" +
		`{"timestamp":"2026-06-18T01:00:00Z","type":"session_meta","payload":{"id":"native_thread"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:01:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"web history ok"}]}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	if err := state.SaveRuntimeSession(ctx, localstate.RuntimeSession{
		SessionID:       "native_thread",
		Runtime:         protocol.RuntimeCodexCLI,
		ProjectID:       "project_1",
		NativeSessionID: "native_thread",
		LastRunID:       "run_1",
		UpdatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save runtime session: %v", err)
	}
	terminal := protocol.RunEvent{
		EventID:   "evt_done_1",
		RunID:     "run_1",
		CommandID: "cmd_1",
		ProjectID: "project_1",
		SessionID: "native_thread",
		Seq:       3,
		Kind:      "run.done",
		At:        time.Now().UTC(),
	}
	if err := state.SaveRunEvent(ctx, terminal); err != nil {
		t.Fatalf("save run event: %v", err)
	}

	result := codexscan.Result{
		Sessions: []codexscan.Session{{
			ID:            "history_session",
			ProjectID:     "project_1",
			Path:          sessionPath,
			UpdatedAt:     time.Now().UTC(),
			Size:          int64(len(content)),
			ContentSHA256: "sha_1",
		}},
		RuntimeSessions: []codexscan.RuntimeSession{{
			Runtime:         protocol.RuntimeCodexCLI,
			ProjectID:       "project_1",
			SessionID:       "history_session",
			NativeSessionID: "native_thread",
			ResumeMode:      "codex_cli_history_index",
			UpdatedAt:       time.Now().UTC(),
		}},
	}
	conn := newMemoryConn()
	client := newClient(
		securestore.Identity{AgentID: "agent_1", DeviceID: "device_1", AgentSecret: "secret", ServerURL: "https://server.test"},
		nil,
		staticScanner{result: result},
		protocolDevice(),
		defaultConfig(),
		state,
		false,
		false,
		nil,
	)
	client.setConn(conn)

	done := make(chan error, 1)
	go func() { done <- client.readLoop(ctx, conn) }()

	if err := client.flushRunEventOutbox(ctx); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}
	waitForWrittenType(t, conn, "run.event")
	historyEnv := waitForWrittenEnvelope(t, conn, "history.items")
	historyBatch := decryptEnvelopeData[protocol.HistoryItems](t, historyEnv)
	if historyBatch.SessionID != "history_session" || len(historyBatch.Items) == 0 {
		t.Fatalf("history batch = %+v", historyBatch)
	}
	if historyBatch.UploadID == "" || historyBatch.BatchIndex != 0 || historyBatch.BatchCount != 1 || !historyBatch.Final {
		t.Fatalf("history batch metadata = %+v", historyBatch)
	}
	if err := conn.pushEncrypted("history.items.ack", "server", "server_device", 1, protocol.HistoryItemsAck{
		SessionID:  historyBatch.SessionID,
		Cursor:     historyBatch.Cursor,
		UploadID:   historyBatch.UploadID,
		BatchIndex: historyBatch.BatchIndex,
		Status:     "ok",
	}, "secret"); err != nil {
		t.Fatal(err)
	}
	waitForWrittenType(t, conn, "sync.cursor")

	waitForCodexHistorySynced(t, ctx, state, terminal.EventID)
	byRun, err := state.RuntimeSessionByRun(ctx, "run_1", protocol.RuntimeCodexCLI)
	if err != nil {
		t.Fatalf("runtime session by run: %v", err)
	}
	if byRun.SessionID != "history_session" || byRun.NativeSessionID != "native_thread" {
		t.Fatalf("runtime session by run = %+v", byRun)
	}

	conn.closeWith(errors.New("done"))
	select {
	case err := <-done:
		if err == nil || err.Error() != "done" {
			t.Fatalf("read loop error = %v, want done", err)
		}
	case <-ctx.Done():
		t.Fatal("read loop did not exit")
	}
}

func waitForCodexHistorySynced(t *testing.T, ctx context.Context, state *localstate.Store, eventID string) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		synced, err := state.CodexHistorySynced(ctx, eventID)
		if err != nil {
			t.Fatalf("history synced state: %v", err)
		}
		if synced {
			return
		}
		select {
		case <-deadline:
			t.Fatal("terminal outbox event did not mark codex history synced")
		case <-ticker.C:
		}
	}
}

type memoryConn struct {
	readCh  chan protocol.Envelope
	writeCh chan protocol.Envelope
	closeCh chan error
	once    sync.Once
}

func newMemoryConn() *memoryConn {
	return &memoryConn{
		readCh:  make(chan protocol.Envelope, 16),
		writeCh: make(chan protocol.Envelope, 16),
		closeCh: make(chan error, 1),
	}
}

func (c *memoryConn) ReadEnvelope(ctx context.Context) (protocol.Envelope, error) {
	select {
	case <-ctx.Done():
		return protocol.Envelope{}, ctx.Err()
	case err := <-c.closeCh:
		return protocol.Envelope{}, err
	case env := <-c.readCh:
		return env, nil
	}
}

func (c *memoryConn) WriteEnvelope(ctx context.Context, env protocol.Envelope) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.writeCh <- env:
		return nil
	}
}

func (c *memoryConn) Close() error {
	c.closeWith(errors.New("closed"))
	return nil
}

func (c *memoryConn) closeWith(err error) {
	c.once.Do(func() { c.closeCh <- err })
}

func (c *memoryConn) pushEncrypted(messageType, agentID, deviceID string, seq uint64, data any, secret string) error {
	env, err := protocol.NewEnvelope(messageType, agentID, deviceID, seq, data)
	if err != nil {
		return err
	}
	env, err = protocol.EncryptEnvelopeData(secret, env)
	if err != nil {
		return err
	}
	env.Signature = security.Sign(secret, unsignedBytes(env))
	c.readCh <- env
	return nil
}

func waitForWrittenType(t *testing.T, conn *memoryConn, messageType string) {
	t.Helper()
	_ = waitForWrittenEnvelope(t, conn, messageType)
}

func waitForWrittenEnvelope(t *testing.T, conn *memoryConn, messageType string) protocol.Envelope {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case env := <-conn.writeCh:
			if env.Type == messageType {
				return env
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", messageType)
		}
	}
}

func waitForWrittenAck(t *testing.T, conn *memoryConn) protocol.Ack {
	t.Helper()
	env := waitForWrittenEnvelope(t, conn, "ack")
	return decryptEnvelopeData[protocol.Ack](t, env)
}

func decryptEnvelopeData[T any](t *testing.T, env protocol.Envelope) T {
	t.Helper()
	decrypted, err := protocol.DecryptEnvelopeData("secret", env)
	if err != nil {
		t.Fatalf("decrypt envelope: %v", err)
	}
	var value T
	if err := json.Unmarshal(decrypted.Data, &value); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return value
}

func assertNoWrittenType(t *testing.T, conn *memoryConn, messageType string, wait time.Duration) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case env := <-conn.writeCh:
			if env.Type == messageType {
				t.Fatalf("unexpected %s envelope", messageType)
			}
		case <-timer.C:
			return
		}
	}
}

type blockingScanner struct{}

func (blockingScanner) Credential() (codexscan.Credential, error) {
	return codexscan.Credential{}, errors.New("not configured")
}

func (blockingScanner) DiscoverRoots() []codexscan.Root {
	return nil
}

func (blockingScanner) Scan(ctx context.Context) (codexscan.Result, error) {
	<-ctx.Done()
	return codexscan.Result{}, ctx.Err()
}

func (blockingScanner) ForEachHistoryChunk(context.Context, codexscan.Session, int, func(codexscan.HistoryChunk) error) error {
	return nil
}

type staticScanner struct {
	result codexscan.Result
}

func (staticScanner) Credential() (codexscan.Credential, error) {
	return codexscan.Credential{}, errors.New("not configured")
}

func (staticScanner) DiscoverRoots() []codexscan.Root {
	return nil
}

func (s staticScanner) Scan(context.Context) (codexscan.Result, error) {
	return s.result, nil
}

func (staticScanner) ForEachHistoryChunk(context.Context, codexscan.Session, int, func(codexscan.HistoryChunk) error) error {
	return nil
}

type sequenceScanner struct {
	mu      sync.Mutex
	results []codexscan.Result
	index   int
}

func (*sequenceScanner) Credential() (codexscan.Credential, error) {
	return codexscan.Credential{}, errors.New("not configured")
}

func (*sequenceScanner) DiscoverRoots() []codexscan.Root {
	return nil
}

func (s *sequenceScanner) Scan(context.Context) (codexscan.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return codexscan.Result{}, nil
	}
	if s.index >= len(s.results) {
		return s.results[len(s.results)-1], nil
	}
	result := s.results[s.index]
	s.index++
	return result, nil
}

func (*sequenceScanner) ForEachHistoryChunk(context.Context, codexscan.Session, int, func(codexscan.HistoryChunk) error) error {
	return nil
}

func protocolDevice() device.Info {
	return device.Info{InstallID: "install_1", MachineHash: "machine_1", Hostname: "host", OS: "test", Arch: "test", AgentVersion: "test"}
}

func defaultConfig() config.Config {
	return config.Config{}
}

func hasFeature(features []string, want string) bool {
	for _, feature := range features {
		if strings.EqualFold(strings.TrimSpace(feature), want) {
			return true
		}
	}
	return false
}

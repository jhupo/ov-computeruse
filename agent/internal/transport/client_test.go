package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/config"
	"ov-computeruse/agent/internal/device"
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

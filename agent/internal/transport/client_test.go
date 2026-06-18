package transport

import (
	"context"
	"errors"
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
		{Runtime: "codex.native", NativeSessionID: "native_1", LastResponseID: "resp_old", UpdatedAt: oldAt},
		{Runtime: "codex.native", NativeSessionID: "native_1", LastResponseID: "resp_new", UpdatedAt: newAt},
		{Runtime: "openai.responses", SessionID: "session_1", NativeSessionID: "responses:resp_1", LastResponseID: "resp_1", UpdatedAt: newAt},
	})
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2: %+v", len(sessions), sessions)
	}
	foundNative := false
	for _, session := range sessions {
		if session.Runtime == "codex.native" {
			foundNative = true
			if session.NativeSessionID != "native_1" || session.LastResponseID != "resp_new" {
				t.Fatalf("native session = %+v", session)
			}
		}
	}
	if !foundNative {
		t.Fatalf("native-only runtime session was dropped: %+v", sessions)
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
	deadline := time.After(time.Second)
	for {
		select {
		case env := <-conn.writeCh:
			if env.Type == messageType {
				return
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

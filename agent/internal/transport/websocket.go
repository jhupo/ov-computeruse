package transport

import (
	"context"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"ov-computeruse/agent/internal/protocol"
)

const maxEnvelopeBytes = 2 << 20

type WebSocketDialer struct {
	Dialer *websocket.Dialer
	Header http.Header
}

func (d WebSocketDialer) Dial(ctx context.Context, endpoint Endpoint) (Conn, error) {
	dialer := d.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	header := d.Header.Clone()
	if endpoint.Token != "" {
		header.Set("Authorization", "Bearer "+endpoint.Token)
	}

	conn, _, err := dialer.DialContext(ctx, endpoint.URL, header)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(maxEnvelopeBytes)
	return &webSocketConn{conn: conn}, nil
}

type webSocketConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *webSocketConn) ReadEnvelope(ctx context.Context) (protocol.Envelope, error) {
	var env protocol.Envelope
	done := make(chan error, 1)
	go func() {
		done <- c.conn.ReadJSON(&env)
	}()

	select {
	case <-ctx.Done():
		return protocol.Envelope{}, ctx.Err()
	case err := <-done:
		return env, err
	}
}

func (c *webSocketConn) WriteEnvelope(ctx context.Context, env protocol.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- c.conn.WriteJSON(env)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *webSocketConn) Close() error {
	return c.conn.Close()
}

package transport

import (
	"context"

	"ov-computeruse/agent/internal/protocol"
)

type Conn interface {
	ReadEnvelope(context.Context) (protocol.Envelope, error)
	WriteEnvelope(context.Context, protocol.Envelope) error
	Close() error
}

type Dialer interface {
	Dial(context.Context, Endpoint) (Conn, error)
}

type Endpoint struct {
	URL   string
	Token string
}

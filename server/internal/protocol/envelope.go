package protocol

import (
	"encoding/json"
	"time"
)

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

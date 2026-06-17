package protocol

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

var ErrInvalidSignature = errors.New("invalid envelope signature")

type Signer interface {
	SignEnvelope(context.Context, Envelope) (Envelope, error)
}

type Verifier interface {
	VerifyEnvelope(context.Context, Envelope) error
}

type Security interface {
	Signer
	Verifier
}

type HMACSecurity struct {
	KeyID  string
	Secret []byte
}

func (s HMACSecurity) SignEnvelope(ctx context.Context, env Envelope) (Envelope, error) {
	if err := ctx.Err(); err != nil {
		return Envelope{}, err
	}
	env.KeyID = s.KeyID
	env.Signature = ""
	body, err := canonicalEnvelopeBytes(env)
	if err != nil {
		return Envelope{}, err
	}
	env.Signature = signHMAC(s.Secret, body)
	return env, nil
}

func (s HMACSecurity) VerifyEnvelope(ctx context.Context, env Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	signature := env.Signature
	env.Signature = ""
	body, err := canonicalEnvelopeBytes(env)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(signature), []byte(signHMAC(s.Secret, body))) {
		return ErrInvalidSignature
	}
	return nil
}

func canonicalEnvelopeBytes(env Envelope) ([]byte, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func signHMAC(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"

	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/runtime"
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
}

type Adapter struct {
	cfg Config
}

type promptPayload struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) NewSession(ctx context.Context, command protocol.Command, sink runtime.Sink) error {
	return a.Send(ctx, command, sink)
}

func (a *Adapter) Resume(ctx context.Context, command protocol.Command, sink runtime.Sink) error {
	return a.Send(ctx, command, sink)
}

func (a *Adapter) Send(ctx context.Context, command protocol.Command, sink runtime.Sink) error {
	prompt, err := commandPrompt(command)
	if err != nil {
		return err
	}
	if strings.TrimSpace(a.cfg.APIKey) == "" {
		return errors.New("openai api key is required")
	}
	model := a.cfg.Model
	if strings.TrimSpace(model) == "" {
		model = "gpt-4.1"
	}

	opts := []option.RequestOption{option.WithAPIKey(a.cfg.APIKey)}
	if strings.TrimSpace(a.cfg.BaseURL) != "" {
		opts = append(opts, option.WithBaseURL(a.cfg.BaseURL))
	}
	client := openai.NewClient(opts...)
	stream := client.Responses.NewStreaming(ctx, responses.ResponseNewParams{
		Model: openai.ResponsesModel(model),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(prompt),
		},
	})
	defer stream.Close()

	for stream.Next() {
		event := stream.Current()
		switch variant := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			if variant.Delta != "" {
				if err := emit(ctx, sink, command, "assistant.message.delta", map[string]string{"text": variant.Delta}); err != nil {
					return err
				}
			}
		case responses.ResponseTextDoneEvent:
			if err := emit(ctx, sink, command, "assistant.message.done", map[string]string{"text": variant.Text}); err != nil {
				return err
			}
		case responses.ResponseCompletedEvent:
			if err := emit(ctx, sink, command, "run.status", map[string]string{"status": "response.completed"}); err != nil {
				return err
			}
		case responses.ResponseErrorEvent:
			return errors.New(variant.Message)
		default:
			if raw := event.RawJSON(); raw != "" {
				if err := emit(ctx, sink, command, "tool.output", map[string]string{"raw": raw}); err != nil {
					return err
				}
			}
		}
	}
	return stream.Err()
}

func (a *Adapter) Stop(ctx context.Context, command protocol.Command) error {
	return nil
}

func commandPrompt(command protocol.Command) (string, error) {
	if len(command.Payload) == 0 {
		return "", errors.New("prompt payload is required")
	}
	var payload promptPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(payload.Text)
	}
	if prompt == "" {
		return "", errors.New("prompt is required")
	}
	return prompt, nil
}

func emit(ctx context.Context, sink runtime.Sink, command protocol.Command, kind string, payload any) error {
	if sink == nil {
		return nil
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      kind,
		Payload:   protocol.Raw(payload),
	})
}

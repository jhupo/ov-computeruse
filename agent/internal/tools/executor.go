package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/runtime"
)

type CallKind string

const (
	CallKindFunction   CallKind = "function_call"
	CallKindLocalShell CallKind = "local_shell_call"
)

type Call struct {
	Kind      CallKind        `json:"kind"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type Result struct {
	CallID   string          `json:"call_id"`
	Output   string          `json:"output"`
	Approved bool            `json:"approved,omitempty"`
	Error    string          `json:"error,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

type Executor struct{}

func NewExecutor() Executor {
	return Executor{}
}

func (Executor) Execute(ctx context.Context, sink runtime.Sink, command protocol.Command, call Call) (Result, error) {
	if strings.TrimSpace(call.CallID) == "" {
		return Result{}, errors.New("tool call id is required")
	}
	normalized := normalizeCall(call)
	result, err := executeNormalized(ctx, sink, command, normalized)
	if err != nil {
		result = Result{CallID: normalized.CallID, Error: err.Error(), Output: errorOutput(normalized, err)}
	}
	if strings.TrimSpace(result.Output) == "" {
		result.Output = resultOutput(result)
	}
	if result.CallID == "" {
		result.CallID = normalized.CallID
	}
	if err := emitToolOutput(ctx, sink, command, normalized, "completed", result); err != nil {
		return result, err
	}
	return result, err
}

func executeNormalized(ctx context.Context, sink runtime.Sink, command protocol.Command, call Call) (Result, error) {
	switch call.Kind {
	case CallKindLocalShell:
		return rejectLocalShell(ctx, sink, command, call)
	case CallKindFunction:
		return rejectFunction(call), nil
	default:
		return rejectFunction(call), nil
	}
}

func rejectLocalShell(ctx context.Context, sink runtime.Sink, command protocol.Command, call Call) (Result, error) {
	request := protocol.ApprovalRequest{
		ID:          protocol.NewID("apr"),
		RunID:       command.RunID,
		ProjectID:   command.ProjectID,
		SessionID:   command.SessionID,
		Category:    "local_shell",
		Action:      "local_shell",
		RiskLevel:   "high",
		Description: "Local shell execution requires approval",
		Payload:     protocol.Raw(call),
		At:          time.Now().UTC(),
	}
	waiter, ok := sink.(runtime.ApprovalWaiter)
	if !ok {
		return rejection(call, "local shell execution is unavailable because approvals are not wired"), nil
	}
	decision, err := waiter.AwaitApproval(ctx, request)
	if err != nil {
		return Result{}, err
	}
	approved := isApproved(decision.Decision)
	if !approved {
		return rejection(call, firstNonEmpty(decision.Reason, "local shell execution was rejected")), nil
	}
	return rejection(call, "local shell execution is approved but no shell runner is registered in this agent build"), nil
}

func rejectFunction(call Call) Result {
	return rejection(call, "function tool is not registered in this agent build")
}

func rejection(call Call, reason string) Result {
	payload := protocol.Raw(map[string]any{
		"ok":      false,
		"error":   reason,
		"tool":    call.Name,
		"kind":    call.Kind,
		"call_id": call.CallID,
	})
	return Result{CallID: call.CallID, Output: string(payload), Error: reason, Payload: payload}
}

func normalizeCall(call Call) Call {
	call.CallID = strings.TrimSpace(call.CallID)
	call.ID = strings.TrimSpace(call.ID)
	call.Name = strings.TrimSpace(call.Name)
	if call.Name == "" && call.Kind == CallKindLocalShell {
		call.Name = "local_shell"
	}
	if call.Name == "" {
		call.Name = string(call.Kind)
	}
	return call
}

func emitToolOutput(ctx context.Context, sink runtime.Sink, command protocol.Command, call Call, status string, result any) error {
	if sink == nil {
		return nil
	}
	payload := map[string]any{
		"tool_call_id": call.CallID,
		"call_id":      call.CallID,
		"type":         call.Kind,
		"name":         call.Name,
		"status":       status,
	}
	if arguments := decodeJSON(call.Arguments); arguments != nil {
		payload["arguments"] = arguments
	}
	if result != nil {
		payload["output"] = result
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      "tool.output",
		Payload:   protocol.Raw(payload),
	})
}

func decodeJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return value
}

func errorOutput(call Call, err error) string {
	return resultOutput(Result{CallID: call.CallID, Error: err.Error()})
}

func resultOutput(result Result) string {
	if len(result.Payload) > 0 {
		return string(result.Payload)
	}
	raw := protocol.Raw(map[string]any{
		"ok":      result.Error == "",
		"error":   result.Error,
		"call_id": result.CallID,
	})
	return string(raw)
}

func isApproved(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approved", "approve", "accepted", "allow", "allowed":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

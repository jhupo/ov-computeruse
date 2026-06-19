package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

type eventMapper struct {
	adapter           *Adapter
	assistantByItemID map[string]string
	terminalByItemID  map[string]string
}

var errUnsupportedApproval = errors.New("codex cli exec approval request is unsupported")

func newEventMapper(adapter *Adapter) *eventMapper {
	return &eventMapper{adapter: adapter, assistantByItemID: map[string]string{}, terminalByItemID: map[string]string{}}
}

func (m *eventMapper) emitEvent(ctx context.Context, command protocol.Command, resolved localstate.CommandContext, event execEvent, sink agentruntime.Sink) error {
	switch event.Type {
	case "thread.started":
		return m.adapter.emitRuntimeSession(ctx, command, resolved, event.ThreadID, sink)
	case "turn.started":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.started", "raw": event.Raw})
	case "turn.completed":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.completed", "usage": rawJSON(event.Usage), "raw": event.Raw})
	case "turn.failed":
		message := firstNonEmpty(event.Message, event.Error.Message)
		if err := emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.failed", "message": message, "raw": event.Raw}); err != nil {
			return err
		}
		return fmt.Errorf("codex CLI turn failed: %s", firstNonEmpty(message, string(event.Raw)))
	case "error":
		return fmt.Errorf("codex CLI error: %s", firstNonEmpty(event.Message, event.Error.Message, string(event.Raw)))
	case "codex/event/exec_approval_request", "codex/event/apply_patch_approval_request", "codex/event/elicitation_request", "exec_approval_request", "apply_patch_approval_request", "elicitation_request":
		return emitUnsupportedApproval(ctx, command, event.Type, event.Raw, sink)
	case "item.started", "item.updated", "item.completed":
		return m.emitItem(ctx, command, event.Type, decodeItem(event.Item), sink)
	default:
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.event", "type": event.Type, "raw": event.Raw})
	}
}

func (m *eventMapper) emitItem(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	switch item.Type {
	case "agent_message":
		return m.emitAgentMessage(ctx, command, phase, item, sink)
	case "reasoning":
		return emitReasoning(ctx, command, phase, item, sink)
	case "command_execution":
		return m.emitCommandExecution(ctx, command, phase, item, sink)
	case "mcp_tool_call":
		return emitTool(ctx, command, phase, item, firstNonEmpty(item.Tool, item.Name, "mcp"), mcpToolPayload(item), sink)
	case "file_change":
		return emitTool(ctx, command, phase, item, "file_change", fileChangePayload(item), sink)
	case "todo_list":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.todo_list", "phase": phase, "items": rawJSON(item.Items), "item": rawJSON(item.Raw)})
	case "mcp_approval_request", "exec_approval_request", "apply_patch_approval_request", "elicitation_request":
		return emitUnsupportedApproval(ctx, command, item.Type, item.Raw, sink)
	case "error":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.item.error", "phase": phase, "error": rawJSON(item.Error), "item": rawJSON(item.Raw)})
	case "web_search", "collab_tool_call":
		return emitTool(ctx, command, phase, item, firstNonEmpty(item.Tool, item.Name, item.Type), genericToolPayload(item), sink)
	default:
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.item", "phase": phase, "item_type": item.Type, "item": rawJSON(item.Raw)})
	}
}

func emitUnsupportedApproval(ctx context.Context, command protocol.Command, sourceType string, raw json.RawMessage, sink agentruntime.Sink) error {
	payload := map[string]any{
		"status":      "codex.approval.unsupported",
		"source_type": sourceType,
		"message":     "Codex CLI exec emitted an approval request. Headless exec does not support remote approval decisions, so the run is failed instead of waiting.",
		"raw":         rawJSON(raw),
	}
	if fields := approvalFields(raw); len(fields) > 0 {
		for key, value := range fields {
			payload[key] = value
		}
	}
	if err := emit(ctx, sink, command, "run.status", payload); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", errUnsupportedApproval, sourceType)
}

func approvalFields(raw json.RawMessage) map[string]any {
	var value map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"id", "approval_id", "call_id", "server", "tool", "name", "command", "cwd", "reason"} {
		if text, ok := value[key].(string); ok && text != "" {
			out[key] = text
		}
	}
	if arguments, ok := value["arguments"]; ok {
		out["arguments"] = arguments
	}
	if action, ok := value["action"]; ok {
		out["action"] = action
	}
	return out
}

func (m *eventMapper) emitCommandExecution(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	payload := commandExecutionPayload(item)
	if err := emitTool(ctx, command, phase, item, "terminal", payload, sink); err != nil {
		return err
	}
	if phase != "item.updated" {
		return nil
	}
	output := firstNonEmpty(item.AggregatedOutput, item.Output)
	if output == "" {
		return nil
	}
	delta := m.terminalOutputDelta(item.ID, output)
	if delta == "" {
		return nil
	}
	return emit(ctx, sink, command, "terminal.output", map[string]any{
		"stream":       "stdout",
		"text":         delta,
		"tool_call_id": item.ID,
		"tool_name":    "terminal",
	})
}

func (m *eventMapper) terminalOutputDelta(itemID string, output string) string {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return output
	}
	previous := m.terminalByItemID[itemID]
	m.terminalByItemID[itemID] = output
	if previous == "" {
		return output
	}
	if strings.HasPrefix(output, previous) {
		return strings.TrimPrefix(output, previous)
	}
	return output
}

func (m *eventMapper) emitAgentMessage(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	if item.Text == "" {
		return nil
	}
	if phase == "item.completed" {
		return emit(ctx, sink, command, "assistant.message.done", map[string]any{"text": item.Text, "item_id": item.ID, "raw": item.Raw})
	}
	text := m.assistantMessageDelta(item.ID, item.Text)
	if text == "" {
		return nil
	}
	return emit(ctx, sink, command, "assistant.message.delta", map[string]any{"text": text, "item_id": item.ID, "raw": item.Raw})
}

func (m *eventMapper) assistantMessageDelta(itemID string, text string) string {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return text
	}
	previous := m.assistantByItemID[itemID]
	m.assistantByItemID[itemID] = text
	if previous == "" {
		return text
	}
	if strings.HasPrefix(text, previous) {
		return strings.TrimPrefix(text, previous)
	}
	return text
}

func emitReasoning(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	return emit(ctx, sink, command, "run.status", map[string]any{
		"status":  "codex.reasoning",
		"phase":   phase,
		"text":    firstNonEmpty(item.Summary, item.Text),
		"item_id": item.ID,
		"item":    rawJSON(item.Raw),
	})
}

func emitTool(ctx context.Context, command protocol.Command, phase string, item execItem, toolName string, payload map[string]any, sink agentruntime.Sink) error {
	payload["id"] = item.ID
	payload["tool_call_id"] = item.ID
	payload["tool_name"] = toolName
	payload["name"] = firstNonEmpty(item.Name, item.Tool, toolName)
	payload["status"] = item.Status
	payload["raw"] = rawJSON(item.Raw)

	switch phase {
	case "item.started":
		return emit(ctx, sink, command, "tool.call.started", payload)
	case "item.updated":
		return emit(ctx, sink, command, "tool.call.delta", payload)
	default:
		if err := emit(ctx, sink, command, "tool.call.done", payload); err != nil {
			return err
		}
		if hasToolOutput(payload) {
			return emit(ctx, sink, command, "tool.output", payload)
		}
		return nil
	}
}

func commandExecutionPayload(item execItem) map[string]any {
	return map[string]any{
		"command": item.Command,
		"output":  firstNonEmpty(item.AggregatedOutput, item.Output),
		"exit_code": func() any {
			if item.ExitCode == nil {
				return nil
			}
			return *item.ExitCode
		}(),
	}
}

func mcpToolPayload(item execItem) map[string]any {
	return map[string]any{
		"server":    item.Server,
		"tool":      item.Tool,
		"arguments": rawJSON(item.Arguments),
		"output":    firstNonEmpty(rawText(item.Result), rawText(item.Error), item.Output, item.Text),
		"result":    rawJSON(item.Result),
		"error":     rawJSON(item.Error),
	}
}

func fileChangePayload(item execItem) map[string]any {
	return map[string]any{
		"changes": rawJSON(item.Changes),
		"output":  firstNonEmpty(rawText(item.Changes), item.Output, item.Text),
	}
}

func genericToolPayload(item execItem) map[string]any {
	return map[string]any{
		"arguments": rawJSON(item.Arguments),
		"output":    firstNonEmpty(item.Output, item.Text, rawText(item.Result), rawText(item.Error)),
		"result":    rawJSON(item.Result),
		"error":     rawJSON(item.Error),
	}
}

func hasToolOutput(payload map[string]any) bool {
	for _, key := range []string{"output", "result", "error", "changes"} {
		if value, ok := payload[key]; ok && value != nil && value != "" {
			return true
		}
	}
	return false
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

func emit(ctx context.Context, sink agentruntime.Sink, command protocol.Command, kind string, payload any) error {
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

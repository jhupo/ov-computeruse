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
	threadID          string
	currentTurnID     string
	projectID         string
	sessionID         string
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
		m.threadID = strings.TrimSpace(event.ThreadID)
		m.sessionID = firstNonEmpty(command.SessionID, resolved.Session.ID, m.threadID)
		m.projectID = firstNonEmpty(command.ProjectID, resolved.Project.ID, resolved.Session.ProjectID)
		m.adapter.active.alias(command.RunID, m.sessionID, m.threadID)
		return m.adapter.emitRuntimeSession(ctx, command, resolved, event.ThreadID, sink)
	case "turn.started":
		m.currentTurnID = firstNonEmpty(event.TurnID, protocol.NewID("turn"))
		return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{"status": "codex.turn.started", "raw": event.Raw}, execItem{}, "turn.started"))
	case "turn.completed":
		return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{"status": "codex.turn.completed", "usage": rawJSON(event.Usage), "raw": event.Raw}, execItem{}, "turn.completed"))
	case "turn.failed":
		message := firstNonEmpty(event.Message, event.Error.Message)
		if err := m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{"status": "codex.turn.failed", "message": message, "raw": event.Raw}, execItem{}, "turn.failed")); err != nil {
			return err
		}
		return fmt.Errorf("codex CLI turn failed: %s", firstNonEmpty(message, string(event.Raw)))
	case "error":
		return fmt.Errorf("codex CLI error: %s", firstNonEmpty(event.Message, event.Error.Message, string(event.Raw)))
	case "codex/event/exec_approval_request", "codex/event/apply_patch_approval_request", "codex/event/elicitation_request", "exec_approval_request", "apply_patch_approval_request", "elicitation_request":
		return m.emitUnsupportedApproval(ctx, command, event.Type, event.Raw, sink)
	case "item.started", "item.updated", "item.completed":
		return m.emitItem(ctx, command, event.Type, decodeItem(event.Item), sink)
	default:
		return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{"status": "codex.event", "type": event.Type, "raw": event.Raw}, execItem{}, event.Type))
	}
}

func (m *eventMapper) emitItem(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	switch item.Type {
	case "agent_message":
		return m.emitAgentMessage(ctx, command, phase, item, sink)
	case "reasoning":
		return m.emitReasoning(ctx, command, phase, item, sink)
	case "command_execution":
		return m.emitCommandExecution(ctx, command, phase, item, sink)
	case "mcp_tool_call":
		return m.emitTool(ctx, command, phase, item, firstNonEmpty(item.Tool, item.Name, "mcp"), mcpToolPayload(item), sink)
	case "file_change":
		return m.emitTool(ctx, command, phase, item, "file_change", fileChangePayload(item), sink)
	case "todo_list":
		return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{"status": "codex.todo_list", "items": rawJSON(item.Items), "item": rawJSON(item.Raw)}, item, phase))
	case "mcp_approval_request", "exec_approval_request", "apply_patch_approval_request", "elicitation_request":
		return m.emitUnsupportedApproval(ctx, command, item.Type, item.Raw, sink)
	case "error":
		return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{
			"status":  "codex.item.error",
			"message": firstNonEmpty(item.Message, item.McpError.Message, rawText(item.Error)),
			"error":   rawJSON(item.Error),
			"item":    rawJSON(item.Raw),
		}, item, phase))
	case "web_search":
		return m.emitTool(ctx, command, phase, item, "web_search", webSearchPayload(item), sink)
	case "collab_tool_call":
		return m.emitTool(ctx, command, phase, item, firstNonEmpty(item.Tool, item.Name, "collab"), collabToolPayload(item), sink)
	default:
		return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{"status": "codex.item", "item": rawJSON(item.Raw)}, item, phase))
	}
}

func (m *eventMapper) enrichPayload(payload map[string]any, item execItem, phase string) map[string]any {
	payload["runtime"] = runtimeName
	if strings.TrimSpace(m.threadID) != "" {
		payload["thread_id"] = strings.TrimSpace(m.threadID)
	}
	if strings.TrimSpace(m.currentTurnID) != "" {
		payload["turn_id"] = strings.TrimSpace(m.currentTurnID)
	}
	if strings.TrimSpace(item.ID) != "" {
		payload["item_id"] = strings.TrimSpace(item.ID)
	}
	if strings.TrimSpace(item.Type) != "" {
		payload["item_type"] = strings.TrimSpace(item.Type)
	}
	if strings.TrimSpace(phase) != "" {
		payload["phase"] = strings.TrimSpace(phase)
	}
	return payload
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

func (m *eventMapper) emitUnsupportedApproval(ctx context.Context, command protocol.Command, sourceType string, raw json.RawMessage, sink agentruntime.Sink) error {
	command.ProjectID = firstNonEmpty(m.projectID, command.ProjectID)
	command.SessionID = firstNonEmpty(m.sessionID, command.SessionID)
	return emitUnsupportedApproval(ctx, command, sourceType, raw, sink)
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
	if err := m.emitTool(ctx, command, phase, item, "terminal", payload, sink); err != nil {
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
	return m.emit(ctx, sink, command, "terminal.output", m.enrichPayload(map[string]any{
		"stream":       "stdout",
		"text":         delta,
		"tool_call_id": item.ID,
		"tool_name":    "terminal",
	}, item, phase))
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
		return m.emit(ctx, sink, command, "assistant.message.done", m.enrichPayload(map[string]any{"text": item.Text, "raw": item.Raw}, item, phase))
	}
	text := m.assistantMessageDelta(item.ID, item.Text)
	if text == "" {
		return nil
	}
	return m.emit(ctx, sink, command, "assistant.message.delta", m.enrichPayload(map[string]any{"text": text, "raw": item.Raw}, item, phase))
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

func (m *eventMapper) emitReasoning(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	return m.emit(ctx, sink, command, "run.status", m.enrichPayload(map[string]any{
		"status": "codex.reasoning",
		"text":   firstNonEmpty(item.Summary, item.Text),
		"item":   rawJSON(item.Raw),
	}, item, phase))
}

func (m *eventMapper) emitTool(ctx context.Context, command protocol.Command, phase string, item execItem, toolName string, payload map[string]any, sink agentruntime.Sink) error {
	payload["id"] = item.ID
	payload["tool_call_id"] = item.ID
	payload["tool_name"] = toolName
	payload["name"] = firstNonEmpty(item.Name, item.Tool, toolName)
	payload["status"] = item.Status
	payload["raw"] = rawJSON(item.Raw)
	payload = m.enrichPayload(payload, item, phase)

	switch phase {
	case "item.started":
		return m.emit(ctx, sink, command, "tool.call.started", payload)
	case "item.updated":
		return m.emit(ctx, sink, command, "tool.call.delta", payload)
	default:
		if err := m.emit(ctx, sink, command, "tool.call.done", payload); err != nil {
			return err
		}
		if hasToolOutput(payload) {
			return m.emit(ctx, sink, command, "tool.output", payload)
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
		"server":             item.Server,
		"tool":               item.Tool,
		"arguments":          rawJSON(item.Arguments),
		"content":            rawJSON(item.McpResult.Content),
		"meta":               rawJSON(item.McpResult.Meta),
		"structured_content": rawJSON(item.McpResult.StructuredContent),
		"error_message":      item.McpError.Message,
		"output": firstNonEmpty(
			item.Output,
			item.Text,
			item.McpError.Message,
			rawText(item.McpResult.StructuredContent),
			rawText(item.McpResult.Content),
			rawText(item.Result),
			rawText(item.Error),
		),
		"result": rawJSON(item.Result),
		"error":  rawJSON(item.Error),
	}
}

func fileChangePayload(item execItem) map[string]any {
	return map[string]any{
		"changes": rawJSON(item.Changes),
		"output":  firstNonEmpty(rawText(item.Changes), item.Output, item.Text),
	}
}

func webSearchPayload(item execItem) map[string]any {
	return map[string]any{
		"query":  item.Query,
		"action": rawJSON(item.Action),
		"output": firstNonEmpty(item.Output, item.Text, rawText(item.Result), rawText(item.Action)),
		"result": rawJSON(item.Result),
		"error":  rawJSON(item.Error),
	}
}

func collabToolPayload(item execItem) map[string]any {
	return map[string]any{
		"tool":                item.Tool,
		"sender_thread_id":    item.SenderThreadID,
		"receiver_thread_ids": rawJSON(item.ReceiverThreadIDs),
		"prompt":              item.Prompt,
		"agents_states":       rawJSON(item.AgentsStates),
		"output":              firstNonEmpty(item.Output, item.Text, rawText(item.Result), rawText(item.Error), rawText(item.AgentsStates)),
		"result":              rawJSON(item.Result),
		"error":               rawJSON(item.Error),
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
	if len(raw) == 0 || isJSONNull(raw) {
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

func (m *eventMapper) emit(ctx context.Context, sink agentruntime.Sink, command protocol.Command, kind string, payload any) error {
	command.ProjectID = firstNonEmpty(m.projectID, command.ProjectID)
	command.SessionID = firstNonEmpty(m.sessionID, command.SessionID)
	return emit(ctx, sink, command, kind, payload)
}

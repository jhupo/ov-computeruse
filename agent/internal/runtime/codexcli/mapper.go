package codexcli

import (
	"context"
	"encoding/json"
	"fmt"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

func (a *Adapter) emitEvent(ctx context.Context, command protocol.Command, resolved localstate.CommandContext, event execEvent, sink agentruntime.Sink) error {
	switch event.Type {
	case "thread.started":
		return a.emitRuntimeSession(ctx, command, resolved, event.ThreadID, sink)
	case "turn.started":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.started", "raw": event.Raw})
	case "turn.completed":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.completed", "usage": rawJSON(event.Usage), "raw": event.Raw})
	case "turn.failed":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.failed", "message": firstNonEmpty(event.Message, event.Error.Message), "raw": event.Raw})
	case "error":
		return fmt.Errorf("codex CLI error: %s", firstNonEmpty(event.Message, event.Error.Message, string(event.Raw)))
	case "item.started", "item.updated", "item.completed":
		return emitItem(ctx, command, event.Type, decodeItem(event.Item), sink)
	default:
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.event", "type": event.Type, "raw": event.Raw})
	}
}

func emitItem(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	switch item.Type {
	case "agent_message":
		return emitAgentMessage(ctx, command, phase, item, sink)
	case "reasoning":
		return emitReasoning(ctx, command, phase, item, sink)
	case "command_execution":
		return emitTool(ctx, command, phase, item, "terminal", commandExecutionPayload(item), sink)
	case "mcp_tool_call":
		return emitTool(ctx, command, phase, item, firstNonEmpty(item.Tool, item.Name, "mcp"), mcpToolPayload(item), sink)
	case "file_change":
		return emitTool(ctx, command, phase, item, "file_change", fileChangePayload(item), sink)
	case "todo_list":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.todo_list", "phase": phase, "items": rawJSON(item.Items), "item": rawJSON(item.Raw)})
	case "error":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.item.error", "phase": phase, "error": rawJSON(item.Error), "item": rawJSON(item.Raw)})
	case "web_search", "collab_tool_call":
		return emitTool(ctx, command, phase, item, firstNonEmpty(item.Tool, item.Name, item.Type), genericToolPayload(item), sink)
	default:
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.item", "phase": phase, "item_type": item.Type, "item": rawJSON(item.Raw)})
	}
}

func emitAgentMessage(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	if item.Text == "" {
		return nil
	}
	payload := map[string]any{"text": item.Text, "item_id": item.ID, "raw": item.Raw}
	if phase == "item.completed" {
		return emit(ctx, sink, command, "assistant.message.done", payload)
	}
	return emit(ctx, sink, command, "assistant.message.delta", payload)
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

import type {
  AgentSummary,
  ApprovalDecisionResponse,
  ApprovalSummary,
  CommandDetailResponse,
  CommandRecord,
  CreateCommandRequest,
  CreateCommandResponse,
  DashLoginRequest,
  DashLoginResponse,
  DashMeResponse,
  HistoryItem,
  HistoryMessage,
  ListAgentsResponse,
  ListApprovalsResponse,
  ListCommandsResponse,
  ListHistoryItemsResponse,
  ListHistoryMessagesResponse,
  ListProjectsResponse,
  ListRunEventsResponse,
  ListRunsResponse,
  ListSessionsResponse,
  DashboardModel,
  ProjectSummary,
  RetryCommandResponse,
  RunEventRecord,
  RunSummary,
  RunTimelineResponse,
  SessionSummary,
  WorkspaceFileResponse,
  WorkspaceGitStatusResponse,
  WorkspaceTreeResponse,
} from "./types";

const now = new Date("2026-06-19T08:00:00.000Z").toISOString();
const agentId = "agt_mock_main";
const projectId = "proj_mock_dash";
const sessionId = "ses_mock_dash";
const runId = "run_mock_dash";
const commandId = "cmd_mock_dash";

const agents: AgentSummary[] = [
  {
    id: agentId,
    workspace_id: "wsp_mock_local",
    user_id: "usr_mock",
    device_id: "dev_mock_laptop",
    hostname: "local-dev",
    os: "windows",
    arch: "amd64",
    version: "dev",
    status: "online",
    last_seen_at: now,
    registered_at: now,
    disabled: false,
    capabilities: {
      supports_runtime: true,
      supports_history: true,
      supports_terminal: true,
      supports_git: true,
      features: [
        "codex.scan",
        "command.new_session",
        "command.resume",
        "command.send",
        "command.stop",
        "approval.decision",
        "workspace.files",
        "git.status",
        "git.diff",
      ],
      max_concurrent_runs: 1,
    },
    credential: {
      base_url_fingerprint: "base_mock",
      key_fingerprint: "key_mock",
      provider: "openai",
      model: "codex",
      source: "mock",
    },
    health: {
      status: "ok",
      credential_ok: true,
      codex_roots: 1,
      codex_roots_missing: 0,
      last_scan_at: now,
    },
  },
];

const projects: ProjectSummary[] = [
  {
    id: projectId,
    agent_id: agentId,
    name: "ov-computeruse",
    path: "C:\\Users\\jhupo-pc\\Desktop\\ov-computeruse",
    last_active_at: now,
    has_agents_md: true,
    git_branch: "main",
    updated_at: now,
    session_count: 1,
  },
];

const sessions: SessionSummary[] = [
  {
    id: sessionId,
    id_source: "runtime_session",
    agent_id: agentId,
    project_id: projectId,
    title: "Dash data layer",
    cwd: "C:\\Users\\jhupo-pc\\Desktop\\ov-computeruse",
    updated_at: now,
    message_count: 2,
    last_message_at: now,
  },
];

const runs: RunSummary[] = [
  {
    id: runId,
    agent_id: agentId,
    command_id: commandId,
    project_id: projectId,
    session_id: sessionId,
    status: "running",
    last_event_seq: 2,
    last_event_at: now,
    started_at: now,
    event_gap_count: 0,
  },
];

const commands: CommandRecord[] = [
  {
    id: commandId,
    agent_id: agentId,
    run_id: runId,
    session_id: sessionId,
    project_id: projectId,
    kind: "command.send",
    payload: { prompt: "mock prompt" },
    status: "dispatched",
    created_at: now,
    dispatched_at: now,
    retry_count: 0,
  },
];

const events: RunEventRecord[] = [
  {
    id: "evt_mock_1",
    agent_id: agentId,
    device_id: "dev_mock_laptop",
    run_id: runId,
    command_id: commandId,
    session_id: sessionId,
    project_id: projectId,
    seq: 1,
    kind: "run.started",
    payload: { status: "running" },
    event_at: now,
    received_at: now,
  },
];

const approvals: ApprovalSummary[] = [
  {
    id: "apr_mock_1",
    agent_id: agentId,
    run_id: runId,
    project_id: projectId,
    session_id: sessionId,
    category: "command",
    action: "shell",
    risk_level: "medium",
    payload: { command: "npm run build" },
    status: "pending",
    requested_at: now,
  },
];

const historyItems: HistoryItem[] = [
  {
    session_id: sessionId,
    index: 1,
    role: "user",
    kind: "message",
    text: "mock prompt",
    at: now,
  },
];

const historyMessages: HistoryMessage[] = [
  {
    session_id: sessionId,
    index: 1,
    role: "user",
    text: "mock prompt",
    at: now,
  },
];

const workspaceEntries = [
  { name: "agent", path: "agent", kind: "directory" },
  { name: "server", path: "server", kind: "directory" },
  { name: "dash", path: "dash", kind: "directory" },
  { name: "README.md", path: "README.md", kind: "file", size: 2048, mod_time: now },
];

export const mockDashboard: DashboardModel = {
  principal: { user_id: "usr_mock", username: "mock", admin: true },
  agents,
  projects,
  sessions,
  runs,
  run_events: events,
  approvals,
  commands,
  selected_agent_id: agentId,
  selected_project_id: projectId,
  selected_session_id: sessionId,
  selected_run_id: runId,
};

export class MockDashApi {
  async login(request: DashLoginRequest): Promise<DashLoginResponse> {
    return {
      token: "mock_dash_session_token",
      expires_at: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString(),
      principal: { user_id: "usr_mock", username: request.username || "mock", admin: true },
    };
  }

  async me(): Promise<DashMeResponse> {
    return { principal: { user_id: "usr_mock", username: "mock", admin: true } };
  }

  async agents(): Promise<ListAgentsResponse> {
    return { agents };
  }

  async projects(agent_id: string): Promise<ListProjectsResponse> {
    return { agent_id, projects: projects.filter((project) => project.agent_id === agent_id) };
  }

  async sessions(agent_id: string, project_id = ""): Promise<ListSessionsResponse> {
    return {
      agent_id,
      project_id,
      sessions: sessions.filter((session) => session.agent_id === agent_id && (!project_id || session.project_id === project_id)),
    };
  }

  async runs(agent_id: string, session_id = ""): Promise<ListRunsResponse> {
    return {
      agent_id,
      session_id,
      runs: runs.filter((run) => run.agent_id === agent_id && (!session_id || run.session_id === session_id)),
    };
  }

  async runEvents(agent_id: string, run_id: string, after_seq = 0): Promise<ListRunEventsResponse> {
    return { agent_id, run_id, events: events.filter((event) => event.run_id === run_id && event.seq > after_seq) };
  }

  async runTimeline(agent_id: string, run_id: string): Promise<RunTimelineResponse> {
    return {
      agent_id,
      run_id,
      timeline: [{ id: "step_mock_1", run_id, kind: "message", status: "running", title: "Mock step", started_at: now }],
      messages: [{ id: "msg_mock_1", run_id, role: "assistant", text: "Mock response", at: now }],
      tool_calls: [],
    };
  }

  async approvals(status = ""): Promise<ListApprovalsResponse> {
    return { approvals: approvals.filter((approval) => !status || approval.status === status) };
  }

  async decideApproval(approval_id: string, decision: "approved" | "rejected"): Promise<ApprovalDecisionResponse> {
    return { approval_id, decision, command: commands[0] };
  }

  async commands(agent_id: string, status = ""): Promise<ListCommandsResponse> {
    return {
      agent_id,
      status,
      commands: commands.filter((command) => command.agent_id === agent_id && (!status || command.status === status)),
    };
  }

  async command(agent_id: string, command_id: string): Promise<CommandDetailResponse> {
    return { agent_id, command: commands.find((command) => command.id === command_id) ?? commands[0] };
  }

  async createCommand(request: CreateCommandRequest): Promise<CreateCommandResponse> {
    const command: CommandRecord = {
      id: request.command.command_id || `cmd_mock_${Date.now()}`,
      agent_id: request.agent_id,
      run_id: request.command.run_id || `run_mock_${Date.now()}`,
      session_id: request.command.session_id,
      project_id: request.command.project_id,
      kind: request.command.kind,
      mode: request.command.mode,
      payload: request.command.payload,
      status: "queued",
      created_at: new Date().toISOString(),
      retry_count: 0,
      idempotency_key: request.command.idempotency_key,
    };
    commands.unshift(command);
    return { command, command_id: command.id, run_id: command.run_id };
  }

  async retryCommand(agent_id: string, command_id: string): Promise<RetryCommandResponse> {
    const command = commands.find((item) => item.id === command_id) ?? commands[0];
    return { agent_id, command: { ...command, status: "queued", retry_count: command.retry_count + 1 } };
  }

  async historyItems(agent_id: string, session_id: string): Promise<ListHistoryItemsResponse> {
    return { agent_id, session_id, items: historyItems.filter((item) => item.session_id === session_id) };
  }

  async historyMessages(agent_id: string, session_id: string): Promise<ListHistoryMessagesResponse> {
    return { agent_id, session_id, messages: historyMessages.filter((item) => item.session_id === session_id) };
  }

  async workspaceTree(agent_id: string, project_id: string, path = ""): Promise<WorkspaceTreeResponse> {
    return { agent_id, project_id, path, request_id: "wsreq_mock_tree", entries: workspaceEntries };
  }

  async workspaceFile(agent_id: string, project_id: string, path: string): Promise<WorkspaceFileResponse> {
    return {
      agent_id,
      project_id,
      path,
      request_id: "wsreq_mock_file",
      file: {
        path,
        size: 46,
        encoding: "utf-8",
        content: `# ${path}\n\nMock workspace file content.\n`,
        mod_time: now,
      },
    };
  }

  async workspaceGitStatus(agent_id: string, project_id: string): Promise<WorkspaceGitStatusResponse> {
    return {
      agent_id,
      project_id,
      request_id: "wsreq_mock_git",
      git: {
        branch: "main",
        clean: false,
        counts: { modified: 2, added: 1, total: 3 },
        files: [
          { path: "dash/src/App.tsx", kind: "modified", worktree: "M" },
          { path: "server/Dockerfile", kind: "modified", worktree: "M" },
          { path: "dash/src/lib/types.ts", kind: "added", worktree: "A" },
        ],
      },
    };
  }
}

export const mockDashApi = new MockDashApi();

export type ISODateString = string;
export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };
export type JsonRecord = Record<string, JsonValue>;

export interface DashPrincipal {
  user_id?: string;
  username?: string;
  admin?: boolean;
}

export type Principal = DashPrincipal;

export interface AuthSession {
  token: string;
  expires_at: ISODateString;
  principal: Principal;
}

export interface DashLoginRequest {
  username: string;
  password: string;
}

export interface DashLoginResponse {
  token: string;
  expires_at: ISODateString;
  principal: DashPrincipal;
}

export interface DashMeResponse {
  principal: DashPrincipal;
}

export interface InstallState {
  installed: boolean;
  service_registered: boolean;
  service_running: boolean;
  autostart_enabled: boolean;
  package_type?: string;
  channel?: string;
  config_dir?: string;
  data_dir?: string;
  state_path?: string;
  state_db_path?: string;
  log_dir?: string;
  codex_home?: string;
  last_start_at?: ISODateString;
  last_install_check_at?: ISODateString;
  last_error?: string;
}

export interface Credential {
  base_url_fingerprint: string;
  key_fingerprint: string;
  provider?: string;
  model?: string;
  source?: string;
}

export interface Capabilities {
  supports_runtime: boolean;
  supports_history: boolean;
  supports_terminal: boolean;
  supports_git: boolean;
  features?: string[];
  max_concurrent_runs: number;
}

export interface Health {
  status: string;
  credential_ok: boolean;
  credential_source?: string;
  base_url_fingerprint?: string;
  key_fingerprint?: string;
  model?: string;
  codex_roots: number;
  codex_roots_missing: number;
  last_scan_at?: ISODateString;
  last_scan_error?: string;
  last_runtime_error?: string;
}

export interface Heartbeat {
  agent_id: string;
  device_id: string;
  status: string;
  running_runs: string[];
  last_event_seq: number;
  at: ISODateString;
  health?: Health;
}

export interface AgentSummary {
  id: string;
  workspace_id: string;
  user_id?: string;
  device_id: string;
  hostname?: string;
  os?: string;
  arch?: string;
  version?: string;
  status?: string;
  last_seen_at?: ISODateString;
  heartbeat?: Heartbeat | JsonRecord;
  capabilities?: Capabilities | JsonRecord;
  credential?: Credential | JsonRecord;
  install_state?: InstallState | JsonRecord;
  registered_at?: ISODateString;
  health?: Health | JsonRecord;
  disabled: boolean;
  disabled_at?: ISODateString;
  disabled_reason?: string;
  agent_disabled_at?: ISODateString;
  agent_disabled_reason?: string;
  device_disabled_at?: ISODateString;
  device_disabled_reason?: string;
}

export interface ProjectSummary {
  id: string;
  agent_id: string;
  name?: string;
  path?: string;
  last_active_at?: ISODateString;
  has_agents_md: boolean;
  git_branch?: string;
  updated_at?: ISODateString;
  session_count: number;
}

export interface SessionSummary {
  id: string;
  id_source?: string;
  agent_id: string;
  project_id?: string;
  title?: string;
  path?: string;
  cwd?: string;
  updated_at?: ISODateString;
  size?: number;
  content_sha256?: string;
  message_count: number;
  last_message_at?: ISODateString;
}

export type RunStatus =
  | "queued"
  | "dispatched"
  | "running"
  | "done"
  | "failed"
  | "error"
  | "stopping"
  | "stopped"
  | "expired"
  | "rejected"
  | "stale"
  | string;

export interface RunSummary {
  id: string;
  agent_id: string;
  command_id?: string;
  project_id?: string;
  session_id?: string;
  status: RunStatus;
  status_reason?: string;
  last_event_seq: number;
  last_event_at?: ISODateString;
  started_at: ISODateString;
  finished_at?: ISODateString;
  event_gap_count: number;
}

export type CommandKind =
  | "command.new_session"
  | "command.resume"
  | "command.send"
  | "command.stop"
  | "command.approval_decision"
  | "command.refresh_index"
  | string;

export interface CommandPayload {
  prompt?: string;
  text?: string;
  [key: string]: JsonValue | undefined;
}

export interface Command {
  command_id?: string;
  run_id?: string;
  kind: CommandKind;
  project_id?: string;
  session_id?: string;
  mode?: string;
  idempotency_key?: string;
  deadline_at?: ISODateString;
  expires_at?: ISODateString;
  payload?: CommandPayload | JsonRecord;
}

export type CommandStatus =
  | "queued"
  | "dispatched"
  | "dispatch_failed"
  | "acked"
  | "done"
  | "failed"
  | "expired"
  | "stopped"
  | "rejected"
  | string;

export interface CommandRecord {
  id: string;
  agent_id: string;
  run_id?: string;
  session_id?: string;
  project_id?: string;
  kind: CommandKind;
  mode?: string;
  payload?: CommandPayload | JsonRecord;
  status: CommandStatus;
  status_reason?: string;
  created_at: ISODateString;
  dispatched_at?: ISODateString;
  acked_at?: ISODateString;
  deadline_at?: ISODateString;
  expires_at?: ISODateString;
  retry_count: number;
  idempotency_key?: string;
}

export interface RunEventRecord {
  id: string;
  agent_id: string;
  device_id: string;
  run_id?: string;
  command_id?: string;
  session_id?: string;
  project_id?: string;
  seq: number;
  kind: string;
  payload?: JsonRecord;
  event_at: ISODateString;
  received_at: ISODateString;
}

export interface RunStep {
  id?: string;
  agent_id?: string;
  run_id?: string;
  seq?: number;
  kind?: string;
  title?: string;
  status?: string;
  text?: string;
  payload?: JsonRecord;
  started_at?: ISODateString;
  finished_at?: ISODateString;
  [key: string]: JsonValue | undefined;
}

export interface RunMessage {
  id?: string;
  agent_id?: string;
  run_id?: string;
  role?: string;
  text?: string;
  content?: string;
  payload?: JsonRecord;
  at?: ISODateString;
  [key: string]: JsonValue | undefined;
}

export interface ToolCall {
  id?: string;
  agent_id?: string;
  run_id?: string;
  name?: string;
  status?: string;
  input?: JsonRecord;
  output?: JsonRecord;
  started_at?: ISODateString;
  finished_at?: ISODateString;
  [key: string]: JsonValue | undefined;
}

export interface ApprovalSummary {
  id: string;
  agent_id: string;
  run_id?: string;
  project_id?: string;
  session_id?: string;
  category?: string;
  action?: string;
  risk_level?: string;
  payload?: JsonRecord;
  status: "pending" | "approved" | "rejected" | string;
  requested_at: ISODateString;
  decided_at?: ISODateString;
  decision?: "approved" | "rejected" | string;
  decision_reason?: string;
  decision_command_id?: string;
  decision_queued_at?: ISODateString;
  decided_by?: string;
}

export interface ApprovalDecisionRequest {
  decision: "approved" | "rejected";
  reason?: string;
}

export interface RuntimeSession {
  id?: string;
  runtime: string;
  project_id?: string;
  session_id?: string;
  native_session_id?: string;
  resume_mode?: string;
  last_run_id?: string;
  updated_at?: ISODateString;
}

export interface HistoryItem {
  session_id: string;
  index: number;
  role?: string;
  kind: string;
  text?: string;
  payload?: JsonRecord;
  source?: string;
  source_event_id?: string;
  at?: ISODateString;
}

export interface HistoryMessage {
  session_id: string;
  index: number;
  role: string;
  text: string;
  at?: ISODateString;
}

export interface ListAgentsResponse {
  agents: AgentSummary[];
}

export interface ListProjectsResponse {
  agent_id: string;
  projects: ProjectSummary[];
}

export interface ListSessionsResponse {
  agent_id: string;
  project_id?: string;
  sessions: SessionSummary[];
}

export interface ListRunsResponse {
  agent_id: string;
  session_id?: string;
  runs: RunSummary[];
}

export interface ListRunEventsResponse {
  agent_id: string;
  run_id: string;
  events: RunEventRecord[];
}

export interface RunTimelineResponse {
  agent_id: string;
  run_id: string;
  timeline: RunStep[];
  messages: RunMessage[];
  tool_calls: ToolCall[];
  runtime_timeline?: RuntimeTimelineItem[];
}

export interface RuntimeTimelineItem {
  id?: string;
  agent_id?: string;
  run_id?: string;
  session_id?: string;
  project_id?: string;
  seq?: number;
  runtime?: string;
  thread_id?: string;
  turn_id?: string;
  item_id?: string;
  item_type?: string;
  phase?: string;
  kind?: string;
  role?: string;
  text?: string;
  status?: string;
  payload?: JsonRecord;
  event_at?: ISODateString;
  received_at?: ISODateString;
  [key: string]: JsonValue | undefined;
}

export interface ListApprovalsResponse {
  approvals: ApprovalSummary[];
}

export interface ListCommandsResponse {
  agent_id: string;
  status?: string;
  commands: CommandRecord[];
}

export interface CommandDetailResponse {
  agent_id: string;
  command: CommandRecord;
}

export interface CreateCommandRequest {
  agent_id: string;
  command: Command;
}

export interface CreateCommandResponse {
  command: CommandRecord;
  command_id: string;
  run_id?: string;
}

export interface RetryCommandResponse {
  agent_id: string;
  command: CommandRecord;
}

export interface ApprovalDecisionResponse {
  approval_id: string;
  decision: "approved" | "rejected";
  command: CommandRecord;
}

export interface WorkspaceEntry {
  name: string;
  path: string;
  kind: "file" | "directory" | string;
  size?: number;
  mod_time?: ISODateString;
  sensitive?: boolean;
}

export interface WorkspaceFile {
  path: string;
  size: number;
  mod_time?: ISODateString;
  sha256?: string;
  encoding: string;
  content?: string;
  truncated?: boolean;
  binary?: boolean;
  sensitive?: boolean;
}

export interface WorkspaceGitCounts {
  modified?: number;
  added?: number;
  deleted?: number;
  renamed?: number;
  untracked?: number;
  conflicted?: number;
  total: number;
}

export interface WorkspaceGitChange {
  path: string;
  old_path?: string;
  index?: string;
  worktree?: string;
  kind: string;
  conflicted?: boolean;
}

export interface WorkspaceGit {
  branch?: string;
  head?: string;
  upstream?: string;
  ahead?: number;
  behind?: number;
  clean: boolean;
  counts: WorkspaceGitCounts;
  files?: WorkspaceGitChange[];
  truncated?: boolean;
}

export interface WorkspaceTreeResponse {
  agent_id: string;
  project_id: string;
  path?: string;
  request_id: string;
  partial?: boolean;
  warnings?: string[];
  entries: WorkspaceEntry[];
}

export interface WorkspaceFileResponse {
  agent_id: string;
  project_id: string;
  path?: string;
  request_id: string;
  partial?: boolean;
  warnings?: string[];
  file: WorkspaceFile;
}

export interface WorkspaceGitStatusResponse {
  agent_id: string;
  project_id: string;
  path?: string;
  request_id: string;
  git: WorkspaceGit;
}

export interface ListRuntimeSessionsResponse {
  agent_id: string;
  session_id?: string;
  runtime_sessions: RuntimeSession[];
}

export interface ListHistoryItemsResponse {
  agent_id: string;
  session_id: string;
  items: HistoryItem[];
}

export interface ListHistoryMessagesResponse {
  agent_id: string;
  session_id: string;
  messages: HistoryMessage[];
}

export interface DashboardModel {
  principal: Principal | null;
  agents: AgentSummary[];
  projects: ProjectSummary[];
  sessions: SessionSummary[];
  runs: RunSummary[];
  run_events: RunEventRecord[];
  approvals: ApprovalSummary[];
  commands: CommandRecord[];
  selected_agent_id?: string;
  selected_project_id?: string;
  selected_session_id?: string;
  selected_run_id?: string;
}

export interface ApiErrorPayload {
  code: string;
  message: string;
}

export interface ApiErrorBody {
  error?: ApiErrorPayload;
  code?: string;
  message?: string;
}

export type DashWsClientMessage =
  | { type: "run.subscribe"; agent_id: string; run_id: string; after_seq?: number; limit?: number }
  | { type: "run.unsubscribe"; agent_id: string; run_id: string }
  | { type: "ping" };

export type DashWsServerMessage =
  | { type: "run.snapshot"; agent_id: string; run_id: string; timeline: RunStep[]; messages: RunMessage[]; tool_calls: ToolCall[]; events: RunEventRecord[]; after_seq?: number }
  | { type: "run.event"; agent_id?: string; device_id?: string; payload?: RunEventRecord | JsonRecord; [key: string]: JsonValue | RunEventRecord | undefined }
  | { type: "pong"; at?: ISODateString }
  | { type: "error"; code: string; message: string }
  | { type: string; agent_id?: string; device_id?: string; payload?: JsonValue; [key: string]: JsonValue | undefined };

export type DashSocketEvent = DashWsServerMessage;

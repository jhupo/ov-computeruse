import { DashApiError, DashHttpClient, type DashHttpClientOptions } from "./http";
import { mockDashApi, mockDashboard } from "./mock";
import { saveDashSession } from "./session";
import type {
  ApprovalDecisionRequest,
  ApprovalDecisionResponse,
  Command,
  CommandDetailResponse,
  CreateCommandRequest,
  CreateCommandResponse,
  DashboardModel,
  DashLoginRequest,
  DashLoginResponse,
  DashMeResponse,
  ListAgentsResponse,
  ListApprovalsResponse,
  ListCommandsResponse,
  ListHistoryItemsResponse,
  ListHistoryMessagesResponse,
  ListProjectsResponse,
  ListRunEventsResponse,
  ListRunsResponse,
  ListSessionsResponse,
  RetryCommandResponse,
  RunTimelineResponse,
  WorkspaceFileResponse,
  WorkspaceGitStatusResponse,
  WorkspaceTreeResponse,
} from "./types";

export class DashApi {
  private readonly http: DashHttpClient;

  constructor(options: DashHttpClientOptions = {}) {
    this.http = new DashHttpClient(options);
  }

  async login(request: DashLoginRequest): Promise<DashLoginResponse> {
    const response = await this.withMock(() => this.http.post<DashLoginResponse>("/api/dash/login", request), () =>
      mockDashApi.login(request),
    );
    saveDashSession(response);
    return response;
  }

  me(): Promise<DashMeResponse> {
    return this.withMock(() => this.http.get<DashMeResponse>("/api/dash/me"), () => mockDashApi.me());
  }

  agents(): Promise<ListAgentsResponse> {
    return this.withMock(() => this.http.get<ListAgentsResponse>("/api/dash/agents"), () => mockDashApi.agents());
  }

  projects(agent_id: string): Promise<ListProjectsResponse> {
    return this.withMock(
      () => this.http.get<ListProjectsResponse>("/api/dash/projects", { agent_id }),
      () => mockDashApi.projects(agent_id),
    );
  }

  sessions(agent_id: string, project_id?: string, limit?: number): Promise<ListSessionsResponse> {
    return this.withMock(
      () => this.http.get<ListSessionsResponse>("/api/dash/sessions", { agent_id, project_id, limit }),
      () => mockDashApi.sessions(agent_id, project_id),
    );
  }

  runs(agent_id: string, session_id?: string, limit?: number): Promise<ListRunsResponse> {
    return this.withMock(
      () => this.http.get<ListRunsResponse>("/api/dash/runs", { agent_id, session_id, limit }),
      () => mockDashApi.runs(agent_id, session_id),
    );
  }

  runEvents(agent_id: string, run_id: string, after_seq?: number, limit?: number): Promise<ListRunEventsResponse> {
    return this.withMock(
      () => this.http.get<ListRunEventsResponse>("/api/dash/runs/events", { agent_id, run_id, after_seq, limit }),
      () => mockDashApi.runEvents(agent_id, run_id, after_seq),
    );
  }

  runTimeline(agent_id: string, run_id: string): Promise<RunTimelineResponse> {
    return this.withMock(
      () => this.http.get<RunTimelineResponse>("/api/dash/runs/timeline", { agent_id, run_id }),
      () => mockDashApi.runTimeline(agent_id, run_id),
    );
  }

  approvals(status?: string, limit?: number): Promise<ListApprovalsResponse> {
    return this.withMock(
      () => this.http.get<ListApprovalsResponse>("/api/dash/approvals", { status, limit }),
      () => mockDashApi.approvals(status),
    );
  }

  decideApproval(approval_id: string, request: ApprovalDecisionRequest): Promise<ApprovalDecisionResponse> {
    return this.withMock(
      () => this.http.post<ApprovalDecisionResponse>(`/api/dash/approvals/${approval_id}/decision`, request),
      () => mockDashApi.decideApproval(approval_id, request.decision),
    );
  }

  commands(agent_id: string, status?: string, limit?: number): Promise<ListCommandsResponse> {
    return this.withMock(
      () => this.http.get<ListCommandsResponse>("/api/dash/commands", { agent_id, status, limit }),
      () => mockDashApi.commands(agent_id, status),
    );
  }

  command(agent_id: string, command_id: string): Promise<CommandDetailResponse> {
    return this.withMock(
      () => this.http.get<CommandDetailResponse>(`/api/dash/commands/${command_id}`, { agent_id }),
      () => mockDashApi.command(agent_id, command_id),
    );
  }

  createCommand(request: CreateCommandRequest): Promise<CreateCommandResponse> {
    return this.withMock(
      () => this.http.post<CreateCommandResponse>("/api/dash/commands", request),
      () => mockDashApi.createCommand(request),
    );
  }

  retryCommand(agent_id: string, command_id: string): Promise<RetryCommandResponse> {
    return this.withMock(
      () => this.http.post<RetryCommandResponse>(`/api/dash/commands/${command_id}/retry`, undefined, { agent_id }),
      () => mockDashApi.retryCommand(agent_id, command_id),
    );
  }

  historyItems(agent_id: string, session_id: string, after_index?: number, limit?: number): Promise<ListHistoryItemsResponse> {
    return this.withMock(
      () => this.http.get<ListHistoryItemsResponse>("/api/dash/history/items", { agent_id, session_id, after_index, limit }),
      () => mockDashApi.historyItems(agent_id, session_id),
    );
  }

  historyMessages(agent_id: string, session_id: string): Promise<ListHistoryMessagesResponse> {
    return this.withMock(
      () => this.http.get<ListHistoryMessagesResponse>("/api/dash/history/messages", { agent_id, session_id }),
      () => mockDashApi.historyMessages(agent_id, session_id),
    );
  }

  workspaceTree(agent_id: string, project_id: string, path = "", depth = 1): Promise<WorkspaceTreeResponse> {
    return this.withMock(
      () => this.http.get<WorkspaceTreeResponse>("/api/dash/workspace/tree", { agent_id, project_id, path, depth, limit: 500 }),
      () => mockDashApi.workspaceTree(agent_id, project_id, path),
    );
  }

  workspaceFile(agent_id: string, project_id: string, path: string): Promise<WorkspaceFileResponse> {
    return this.withMock(
      () => this.http.get<WorkspaceFileResponse>("/api/dash/workspace/file", { agent_id, project_id, path, max_bytes: 262144 }),
      () => mockDashApi.workspaceFile(agent_id, project_id, path),
    );
  }

  workspaceGitStatus(agent_id: string, project_id: string): Promise<WorkspaceGitStatusResponse> {
    return this.withMock(
      () => this.http.get<WorkspaceGitStatusResponse>("/api/dash/workspace/git-status", { agent_id, project_id, limit: 200 }),
      () => mockDashApi.workspaceGitStatus(agent_id, project_id),
    );
  }

  async loadDashboardBootstrap(): Promise<DashboardModel> {
    return this.withMock(async () => {
      const me = await this.me();
      const { agents } = await this.agents();
      const selected_agent_id = agents[0]?.id;

      if (!selected_agent_id) {
        const { approvals } = await this.approvals("pending");
        return {
          principal: me.principal,
          agents,
          projects: [],
          sessions: [],
          runs: [],
          run_events: [],
          approvals,
          commands: [],
        };
      }

      const [{ projects }, { approvals }, { commands }] = await Promise.all([
        this.projects(selected_agent_id),
        this.approvals("pending"),
        this.commands(selected_agent_id),
      ]);
      const selected_project_id = projects[0]?.id;
      const { sessions } = await this.sessions(selected_agent_id, selected_project_id);
      const selected_session_id = sessions[0]?.id;
      const { runs } = await this.runs(selected_agent_id, selected_session_id);
      const selected_run_id = runs[0]?.id;
      const runEvents = selected_run_id ? await this.runEvents(selected_agent_id, selected_run_id) : undefined;

      return {
        principal: me.principal,
        agents,
        projects,
        sessions,
        runs,
        run_events: runEvents?.events ?? [],
        approvals,
        commands,
        selected_agent_id,
        selected_project_id,
        selected_session_id,
        selected_run_id,
      };
    }, async () => mockDashboard);
  }

  private async withMock<T>(request: () => Promise<T>, fallback: () => Promise<T>): Promise<T> {
    try {
      return await request();
    } catch (error) {
      if (!this.http.mockFallback || error instanceof DashApiError) {
        throw error;
      }
      return fallback();
    }
  }
}

export const dashApi = new DashApi();

export function login(request: DashLoginRequest): Promise<DashLoginResponse> {
  return dashApi.login(request);
}

export function getMe(): Promise<DashMeResponse> {
  return dashApi.me();
}

export function loadDashboardBootstrap(): Promise<DashboardModel> {
  return dashApi.loadDashboardBootstrap();
}

export function sendCommand(agent_id: string, command: Command): Promise<CreateCommandResponse> {
  return dashApi.createCommand({ agent_id, command });
}

export function loadWorkspaceTree(agent_id: string, project_id: string, path?: string): Promise<WorkspaceTreeResponse> {
  return dashApi.workspaceTree(agent_id, project_id, path);
}

export function loadWorkspaceFile(agent_id: string, project_id: string, path: string): Promise<WorkspaceFileResponse> {
  return dashApi.workspaceFile(agent_id, project_id, path);
}

export function loadWorkspaceGitStatus(agent_id: string, project_id: string): Promise<WorkspaceGitStatusResponse> {
  return dashApi.workspaceGitStatus(agent_id, project_id);
}

export function decideApproval(approval_id: string, request: ApprovalDecisionRequest): Promise<ApprovalDecisionResponse> {
  return dashApi.decideApproval(approval_id, request);
}

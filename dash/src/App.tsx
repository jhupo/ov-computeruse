import { useEffect, useMemo, useState } from "react";
import type { ElementType, FormEvent, ReactNode } from "react";
import {
  Activity,
  Bell,
  Bot,
  Check,
  ArrowUp,
  ChevronDown,
  Circle,
  Clock3,
  Code2,
  Command,
  FileText,
  Files,
  Folder,
  GitBranch,
  Globe2,
  History,
  Loader2,
  LogOut,
  Menu,
  MessageSquare,
  Moon,
  PanelRight,
  Play,
  Plus,
  RefreshCw,
  Search,
  SendHorizonal,
  Settings,
  ShieldCheck,
  Terminal,
  User,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";
import {
  AgentSummary,
  AuthSession,
  DashboardModel,
  JsonRecord,
  ProjectSummary,
  RunEventRecord,
  RunSummary,
  SessionSummary,
  WorkspaceEntry,
  WorkspaceFile,
  WorkspaceGit,
  clearStoredAuth,
  createDashSocket,
  getStoredAuth,
  loadWorkspaceFile,
  loadWorkspaceGitStatus,
  loadWorkspaceTree,
  loadDashboardBootstrap,
  login,
  sendCommand,
} from "@/lib";
import { createTranslator, detectInitialLocale, Locale, localeNames } from "@/i18n";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { Progress } from "@/components/ui/progress";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

type ViewMode = "chat" | "workspace" | "activity";

const localeStorageKey = "ov-dash-locale";

export function App() {
  const [locale, setLocale] = useState<Locale>(() => {
    const stored = window.localStorage.getItem(localeStorageKey);
    return stored === "zh-CN" || stored === "en-US" ? stored : detectInitialLocale();
  });
  const t = useMemo(() => createTranslator(locale), [locale]);
  const [auth, setAuth] = useState<AuthSession | null>(() => getStoredAuth());
  const [model, setModel] = useState<DashboardModel | null>(null);
  const [activeAgentId, setActiveAgentId] = useState<string>("");
  const [activeProjectId, setActiveProjectId] = useState<string>("");
  const [activeSessionId, setActiveSessionId] = useState<string>("");
  const [activeRunId, setActiveRunId] = useState<string>("");
  const [viewMode, setViewMode] = useState<ViewMode>("chat");
  const [isLoading, setIsLoading] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [mobileInsightOpen, setMobileInsightOpen] = useState(false);

  useEffect(() => {
    window.localStorage.setItem(localeStorageKey, locale);
  }, [locale]);

  useEffect(() => {
    if (!auth) {
      setModel(null);
      return;
    }

    let cancelled = false;
    setIsLoading(true);
    setLoadError("");
    loadDashboardBootstrap()
      .then((nextModel) => {
        if (cancelled) return;
        setModel(nextModel);
        const firstAgent = nextModel.selected_agent_id ?? nextModel.agents[0]?.id ?? "";
        const firstProject = nextModel.selected_project_id ?? nextModel.projects[0]?.id ?? "";
        const firstSession = nextModel.selected_session_id ?? nextModel.sessions[0]?.id ?? "";
        const firstRun = nextModel.selected_run_id ?? nextModel.runs[0]?.id ?? "";
        setActiveAgentId((current) => current || firstAgent);
        setActiveProjectId((current) => current || firstProject);
        setActiveSessionId((current) => current || firstSession);
        setActiveRunId((current) => current || firstRun);
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setLoadError(error instanceof Error ? error.message : t("errors.loadFailed"));
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [auth, t]);

  const switchLocale = (nextLocale: Locale) => setLocale(nextLocale);

  const handleAuthenticated = (nextAuth: AuthSession) => {
    setAuth(nextAuth);
  };

  const handleLogout = () => {
    clearStoredAuth();
    setAuth(null);
    setActiveAgentId("");
    setActiveProjectId("");
    setActiveSessionId("");
    setActiveRunId("");
  };

  const selectedAgentId = activeAgentId || model?.selected_agent_id || "";
  const selectedProjectId = activeProjectId || model?.selected_project_id || "";
  const selectedSessionId = activeSessionId || model?.selected_session_id || "";
  const selectedRunId = activeRunId || model?.selected_run_id || "";
  const activeAgent = model?.agents.find((agent) => agent.id === selectedAgentId) ?? model?.agents[0];
  const activeProject = model?.projects.find((project) => project.id === selectedProjectId) ?? model?.projects[0];
  const activeSession = model?.sessions.find((session) => session.id === selectedSessionId) ?? model?.sessions[0];
  const activeRun = model?.runs.find((run) => run.id === selectedRunId) ?? model?.runs[0];

  useEffect(() => {
    if (!auth || !activeAgent?.id || !activeRun?.id) {
      return;
    }

    const currentEvents = model?.run_events.filter((event) => event.run_id === activeRun.id) ?? [];
    const afterSeq = currentEvents.reduce((highest, event) => Math.max(highest, event.seq), 0);
    const socket = createDashSocket({ token: auth.token });
    const unsubscribe = socket.subscribe((message) => {
      if (message.type === "run.snapshot") {
        const payload = messagePayload(message);
        const events = Array.isArray(payload?.events) ? payload.events : Array.isArray(message.events) ? message.events : [];
        setModel((current) =>
          current
            ? mergeRunEvents(
                current,
                events.map((event) => normalizeRunEvent(event, message.agent_id)).filter(isRunEventRecord),
              )
            : current,
        );
        return;
      }

      if (message.type === "run.event") {
        const event = normalizeRunEvent(messagePayload(message) ?? message.payload, message.agent_id);
        if (event) {
          setModel((current) => (current ? mergeRunEvents(current, [event]) : current));
        }
      }
    });

    socket.subscribeRun(activeAgent.id, activeRun.id, afterSeq);

    return () => {
      unsubscribe();
      socket.unsubscribeRun(activeAgent.id, activeRun.id);
      socket.close();
    };
  }, [auth, activeAgent?.id, activeRun?.id]);

  if (!auth) {
    return <LoginScreen locale={locale} onLocaleChange={switchLocale} onAuthenticated={handleAuthenticated} />;
  }

  return (
    <TooltipProvider delayDuration={200}>
      <div className="min-h-dvh bg-background text-foreground">
        <DesktopTitleBar
          locale={locale}
          onLocaleChange={switchLocale}
          onLogout={handleLogout}
          principalName={auth.principal.username || t("auth.admin")}
        />
        <ResizablePanelGroup orientation="horizontal" className="h-[calc(100dvh-36px)] overflow-hidden">
          <ResizablePanel defaultSize={13} minSize={12} maxSize={20} className="hidden bg-[#1b1e22] lg:block">
            <NavigationPanel
              t={t}
              model={model}
              activeAgentId={activeAgent?.id ?? selectedAgentId}
              activeProjectId={activeProject?.id ?? selectedProjectId}
              activeSessionId={activeSession?.id ?? selectedSessionId}
              onAgentChange={setActiveAgentId}
              onProjectChange={setActiveProjectId}
              onSessionChange={setActiveSessionId}
            />
          </ResizablePanel>
          <ResizableHandle className="hidden lg:flex" />

          <ResizablePanel defaultSize={71} minSize={45} className="min-w-0">
            <div className="flex h-full min-w-0 flex-col">
            <MobileHeader
              t={t}
              model={model}
              mobileNavOpen={mobileNavOpen}
              setMobileNavOpen={setMobileNavOpen}
              mobileInsightOpen={mobileInsightOpen}
              setMobileInsightOpen={setMobileInsightOpen}
              activeAgentId={activeAgent?.id ?? selectedAgentId}
              activeProjectId={activeProject?.id ?? selectedProjectId}
              activeSessionId={activeSession?.id ?? selectedSessionId}
              onAgentChange={setActiveAgentId}
              onProjectChange={setActiveProjectId}
              onSessionChange={setActiveSessionId}
              insightPanel={
                <InsightPanel
                  t={t}
                  model={model}
                  activeAgent={activeAgent}
                  activeProject={activeProject}
                  activeRun={activeRun}
                  loading={isLoading}
                />
              }
            />

            <ConversationWorkspace
              t={t}
              model={model}
              activeAgent={activeAgent}
              activeProject={activeProject}
              activeSession={activeSession}
              activeRun={activeRun}
              activeRunId={activeRun?.id ?? ""}
              viewMode={viewMode}
              onViewModeChange={setViewMode}
              onRunChange={setActiveRunId}
              loading={isLoading}
              error={loadError}
            />
            </div>
          </ResizablePanel>

          <ResizableHandle className="hidden xl:flex" />
          <ResizablePanel defaultSize={16} minSize={14} maxSize={24} className="hidden bg-[#191b1f] xl:block">
            <InsightPanel
              t={t}
              model={model}
              activeAgent={activeAgent}
              activeProject={activeProject}
              activeRun={activeRun}
              loading={isLoading}
            />
          </ResizablePanel>
        </ResizablePanelGroup>
      </div>
    </TooltipProvider>
  );
}

function LoginScreen({
  locale,
  onLocaleChange,
  onAuthenticated,
}: {
  locale: Locale;
  onLocaleChange: (locale: Locale) => void;
  onAuthenticated: (auth: AuthSession) => void;
}) {
  const t = useMemo(() => createTranslator(locale), [locale]);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");
    setLoading(true);
    try {
      const auth = await login({ username, password });
      onAuthenticated(auth);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("errors.loginFailed"));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="min-h-dvh bg-[#111315] text-foreground">
      <div className="flex min-h-dvh flex-col">
        <header className="flex h-14 items-center justify-between border-b border-border px-4 sm:px-6">
            <div className="flex items-center gap-2 text-sm font-semibold">
              <span className="flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
                <Command className="size-4" />
              </span>
              <span>{t("brand.name")}</span>
            </div>
            <LanguageMenu locale={locale} onLocaleChange={onLocaleChange} />
        </header>
        <main className="flex flex-1 items-center justify-center px-4 py-8">
          <Card className="w-full max-w-[380px] border-border bg-card shadow-panel">
            <CardHeader className="space-y-1">
              <CardTitle className="text-xl">{t("auth.signIn")}</CardTitle>
            </CardHeader>
            <CardContent>
              <form className="space-y-4" onSubmit={submit}>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="username">
                    {t("auth.username")}
                  </label>
                  <Input
                    id="username"
                    autoComplete="username"
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                    placeholder={t("auth.usernamePlaceholder")}
                  />
                </div>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="password">
                    {t("auth.password")}
                  </label>
                  <Input
                    id="password"
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder={t("auth.passwordPlaceholder")}
                  />
                </div>
                {error ? (
                  <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                    {error}
                  </div>
                ) : null}
                <Button className="w-full" type="submit" disabled={loading}>
                  {loading ? <Loader2 className="mr-2 size-4 animate-spin" /> : <ShieldCheck className="mr-2 size-4" />}
                  {t("auth.signInAction")}
                </Button>
              </form>
            </CardContent>
          </Card>
        </main>
      </div>
    </div>
  );
}

function DesktopTitleBar({
  locale,
  onLocaleChange,
  onLogout,
  principalName,
}: {
  locale: Locale;
  onLocaleChange: (locale: Locale) => void;
  onLogout: () => void;
  principalName: string;
}) {
  const t = useMemo(() => createTranslator(locale), [locale]);

  return (
    <header className="app-region-drag hidden h-9 items-center justify-between border-b border-border bg-[#1b1d21] px-3 text-sm text-muted-foreground lg:flex">
      <div className="flex items-center gap-4">
        <div className="flex items-center gap-2">
          <span className="size-3 rounded-sm border border-muted-foreground/60" />
          <span className="h-3 w-px bg-border" />
          <span>{t("menu.file")}</span>
          <span>{t("menu.edit")}</span>
          <span>{t("menu.view")}</span>
          <span>{t("menu.window")}</span>
          <span>{t("menu.help")}</span>
        </div>
        <div className="app-region-no-drag ml-8 flex h-7 min-w-[260px] items-center gap-2 rounded-md bg-accent px-3 text-xs text-accent-foreground">
          <Command className="size-3.5 text-primary" />
          <span className="truncate">{t("brand.name")}</span>
          <Badge variant="secondary" className="ml-auto h-5 rounded-sm px-1.5">
            {t("status.connected")}
          </Badge>
        </div>
      </div>
      <div className="app-region-no-drag flex items-center gap-2">
        <LanguageMenu locale={locale} onLocaleChange={onLocaleChange} />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label={t("settings.account")}>
              <User className="size-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuLabel>{principalName}</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={onLogout}>
              <LogOut className="mr-2 size-4" />
              {t("auth.logout")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
}

function MobileHeader({
  t,
  model,
  mobileNavOpen,
  setMobileNavOpen,
  mobileInsightOpen,
  setMobileInsightOpen,
  activeAgentId,
  activeProjectId,
  activeSessionId,
  onAgentChange,
  onProjectChange,
  onSessionChange,
  insightPanel,
}: {
  t: ReturnType<typeof createTranslator>;
  model: DashboardModel | null;
  mobileNavOpen: boolean;
  setMobileNavOpen: (open: boolean) => void;
  mobileInsightOpen: boolean;
  setMobileInsightOpen: (open: boolean) => void;
  activeAgentId: string;
  activeProjectId: string;
  activeSessionId: string;
  onAgentChange: (id: string) => void;
  onProjectChange: (id: string) => void;
  onSessionChange: (id: string) => void;
  insightPanel: ReactNode;
}) {
  return (
    <header className="flex h-12 items-center justify-between border-b border-border bg-[#181a1e] px-3 lg:hidden">
      <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
        <SheetTrigger asChild>
          <Button variant="ghost" size="icon" aria-label={t("nav.open")}>
            <Menu className="size-5" />
          </Button>
        </SheetTrigger>
        <SheetContent side="left" className="w-[86vw] max-w-[340px] p-0">
          <SheetHeader className="sr-only">
            <SheetTitle>{t("nav.projects")}</SheetTitle>
          </SheetHeader>
          <NavigationPanel
            t={t}
            model={model}
            activeAgentId={activeAgentId}
            activeProjectId={activeProjectId}
            activeSessionId={activeSessionId}
            onAgentChange={(id) => {
              onAgentChange(id);
              setMobileNavOpen(false);
            }}
            onProjectChange={onProjectChange}
            onSessionChange={(id) => {
              onSessionChange(id);
              setMobileNavOpen(false);
            }}
          />
        </SheetContent>
      </Sheet>

      <div className="flex min-w-0 items-center gap-2 text-sm font-semibold">
        <Command className="size-4 text-primary" />
        <span className="truncate">{t("brand.name")}</span>
      </div>

      <Sheet open={mobileInsightOpen} onOpenChange={setMobileInsightOpen}>
        <SheetTrigger asChild>
          <Button variant="ghost" size="icon" aria-label={t("insights.open")}>
            <PanelRight className="size-5" />
          </Button>
        </SheetTrigger>
        <SheetContent side="right" className="w-[88vw] max-w-[360px] p-0">
          <SheetHeader className="sr-only">
            <SheetTitle>{t("insights.title")}</SheetTitle>
          </SheetHeader>
          {insightPanel}
        </SheetContent>
      </Sheet>
    </header>
  );
}

function NavigationPanel({
  t,
  model,
  activeAgentId,
  activeProjectId,
  activeSessionId,
  onAgentChange,
  onProjectChange,
  onSessionChange,
}: {
  t: ReturnType<typeof createTranslator>;
  model: DashboardModel | null;
  activeAgentId: string;
  activeProjectId: string;
  activeSessionId: string;
  onAgentChange: (id: string) => void;
  onProjectChange: (id: string) => void;
  onSessionChange: (id: string) => void;
}) {
  const agents = model?.agents ?? [];
  const projects = model?.projects ?? [];
  const sessions = model?.sessions ?? [];

  return (
    <div className="flex h-full flex-col">
      <div className="space-y-1 px-2 py-3">
        <NavAction icon={MessageSquare} label={t("nav.quickChat")} active />
        <NavAction icon={Search} label={t("nav.search")} />
        <NavAction icon={Code2} label={t("nav.plugins")} />
        <NavAction icon={Clock3} label={t("nav.automation")} />
      </div>
      <Separator />
      <ScrollArea className="min-h-0 flex-1">
        <div className="px-2 py-3">
          <SectionLabel>{t("nav.agents")}</SectionLabel>
          <div className="space-y-1">
            {agents.map((agent) => (
              <SidebarRow
                key={agent.id}
                icon={agent.disabled ? WifiOff : Bot}
                label={agent.hostname || agent.id}
                meta={agentStatusLabel(t, agent)}
                active={agent.id === activeAgentId}
                status={agent.disabled ? "muted" : agent.status === "online" ? "good" : "warn"}
                onClick={() => onAgentChange(agent.id)}
              />
            ))}
          </div>
        </div>
        <div className="px-2 py-2">
          <SectionLabel>{t("nav.projects")}</SectionLabel>
          <div className="space-y-1">
            {projects.map((project) => (
              <SidebarRow
                key={project.id}
                icon={Folder}
                label={project.name || project.path || project.id}
                meta={project.git_branch}
                active={project.id === activeProjectId}
                onClick={() => onProjectChange(project.id)}
              />
            ))}
          </div>
        </div>
        <div className="px-2 py-2">
          <SectionLabel>{t("nav.conversations")}</SectionLabel>
          <div className="space-y-1">
            {sessions.map((session) => (
              <SidebarRow
                key={session.id}
                icon={History}
                label={session.title || session.id}
                meta={formatRelativeTime(session.updated_at || session.last_message_at, t)}
                active={session.id === activeSessionId}
                onClick={() => onSessionChange(session.id)}
              />
            ))}
          </div>
        </div>
      </ScrollArea>
      <Separator />
      <div className="px-2 py-3">
        <NavAction icon={Settings} label={t("settings.title")} />
      </div>
    </div>
  );
}

function ConversationWorkspace({
  t,
  model,
  activeAgent,
  activeProject,
  activeSession,
  activeRun,
  activeRunId,
  viewMode,
  onViewModeChange,
  onRunChange,
  loading,
  error,
}: {
  t: ReturnType<typeof createTranslator>;
  model: DashboardModel | null;
  activeAgent?: AgentSummary;
  activeProject?: ProjectSummary;
  activeSession?: SessionSummary;
  activeRun?: RunSummary;
  activeRunId: string;
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  onRunChange: (id: string) => void;
  loading: boolean;
  error: string;
}) {
  const [prompt, setPrompt] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const events = model?.run_events.filter((event) => !activeRunId || event.run_id === activeRunId) ?? [];
  const runs = model?.runs ?? [];

  const handleSend = async () => {
    const text = prompt.trim();
    if (!text || !activeAgent) return;
    setSubmitting(true);
    try {
      const response = await sendCommand(activeAgent.id, {
        kind: activeSession ? "command.send" : "command.new_session",
        project_id: activeProject?.id,
        session_id: activeSession?.id,
        payload: { prompt: text },
      });
      if (response.run_id) {
        onRunChange(response.run_id);
      }
      setPrompt("");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[#121416]">
      <div className="flex min-h-14 items-center justify-between border-b border-border px-3 sm:px-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <span className="truncate">{activeSession?.title || t("conversation.untitled")}</span>
            <Badge variant="secondary" className="hidden sm:inline-flex">
              {activeProject?.name || t("conversation.noProject")}
            </Badge>
          </div>
          <div className="mt-1 flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
            <span className="truncate">{activeProject?.path || activeSession?.cwd || t("conversation.waitingForData")}</span>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <Tabs value={viewMode} onValueChange={(value) => onViewModeChange(value as ViewMode)} className="hidden sm:block">
            <TabsList className="h-8">
              <TabsTrigger value="chat" className="h-7 px-2">
                <MessageSquare className="size-4" />
              </TabsTrigger>
              <TabsTrigger value="workspace" className="h-7 px-2">
                <Files className="size-4" />
              </TabsTrigger>
              <TabsTrigger value="activity" className="h-7 px-2">
                <Activity className="size-4" />
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <RunPicker t={t} runs={runs} activeRunId={activeRun?.id ?? ""} onRunChange={onRunChange} />
        </div>
      </div>

      {error ? (
        <div className="border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive">{error}</div>
      ) : null}

      <Tabs value={viewMode} onValueChange={(value) => onViewModeChange(value as ViewMode)} className="flex min-h-0 flex-1 flex-col">
        <TabsContent value="chat" className="m-0 min-h-0 flex-1">
          <ScrollArea className="h-full thin-scrollbar">
            <div className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 py-5 sm:px-6">
              {loading && events.length === 0 ? <LoadingConversation t={t} /> : null}
              {events.length > 0 ? (
                events.map((event) => <EventBlock key={event.id || `${event.run_id}-${event.seq}`} event={event} t={t} />)
              ) : !loading ? (
                <EmptyConversation t={t} />
              ) : null}
            </div>
          </ScrollArea>
        </TabsContent>
        <TabsContent value="workspace" className="m-0 min-h-0 flex-1">
          <WorkspacePane t={t} agent={activeAgent} project={activeProject} />
        </TabsContent>
        <TabsContent value="activity" className="m-0 min-h-0 flex-1">
          <ActivityPane t={t} events={events} />
        </TabsContent>
      </Tabs>

      <Composer
        t={t}
        project={activeProject}
        value={prompt}
        disabled={!activeAgent || submitting}
        submitting={submitting}
        onChange={setPrompt}
        onSend={handleSend}
      />
    </div>
  );
}

function InsightPanel({
  t,
  model,
  activeAgent,
  activeProject,
  activeRun,
  loading,
}: {
  t: ReturnType<typeof createTranslator>;
  model: DashboardModel | null;
  activeAgent?: AgentSummary;
  activeProject?: ProjectSummary;
  activeRun?: RunSummary;
  loading: boolean;
}) {
  const approvals = model?.approvals ?? [];
  const pendingApprovals = approvals.filter((approval) => approval.status === "pending");
  const commandCount = model?.commands.length ?? 0;
  const agentHealth = getAgentHealth(activeAgent);
  const progressItems = [
    { label: t("progress.audit"), done: true },
    { label: t("progress.heartbeat"), done: Boolean(activeAgent) },
    { label: t("progress.tests"), done: activeRun?.status === "done" },
    { label: t("progress.next"), done: false },
  ];

  return (
    <ScrollArea className="h-full thin-scrollbar">
      <div className="space-y-4 p-4">
        <Card className="border-border bg-card">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center justify-between text-sm">
              <span>{t("insights.progress")}</span>
              {loading ? <Loader2 className="size-4 animate-spin text-muted-foreground" /> : null}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {progressItems.map((item) => (
              <div key={item.label} className="flex items-center gap-2 text-sm text-muted-foreground">
                {item.done ? <Check className="size-4 text-muted-foreground" /> : <Circle className="size-4" />}
                <span>{item.label}</span>
              </div>
            ))}
          </CardContent>
        </Card>

        <Card className="border-border bg-card">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center justify-between text-sm">
              <span>{t("insights.environment")}</span>
              <Settings className="size-4 text-muted-foreground" />
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <InsightRow icon={Files} label={t("insights.changes")} value={formatDiffStat(model)} valueClassName="text-success" />
            <InsightRow icon={activeAgent?.disabled ? WifiOff : Wifi} label={t("insights.agent")} value={agentHealth} />
            <InsightRow icon={GitBranch} label={t("insights.branch")} value={activeProject?.git_branch || t("common.unknown")} />
            <InsightRow icon={Terminal} label={t("insights.commands")} value={String(commandCount)} />
          </CardContent>
        </Card>

        <Card className="border-border bg-card">
          <CardHeader className="pb-3">
            <CardTitle className="text-sm">{t("insights.subagents")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {(model?.agents ?? []).slice(0, 6).map((agent, index) => (
              <div key={agent.id} className="flex items-center justify-between text-sm">
                <div className="flex min-w-0 items-center gap-2">
                  <Bot className={cn("size-4", index % 3 === 0 ? "text-primary" : index % 3 === 1 ? "text-success" : "text-warning")} />
                  <span className="truncate">{agent.hostname || agent.id}</span>
                </div>
                <span className={cn("text-xs", agent.disabled ? "text-destructive" : "text-success")}>
                  {agent.disabled ? t("status.disabled") : agentStatusLabel(t, agent)}
                </span>
              </div>
            ))}
          </CardContent>
        </Card>

        <Card className="border-border bg-card">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center justify-between text-sm">
              <span>{t("approvals.title")}</span>
              <Badge variant={pendingApprovals.length ? "destructive" : "secondary"}>{pendingApprovals.length}</Badge>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {pendingApprovals.length ? (
              pendingApprovals.slice(0, 3).map((approval) => <ApprovalItem key={approval.id} t={t} approvalId={approval.id} />)
            ) : (
              <p className="text-sm text-muted-foreground">{t("approvals.empty")}</p>
            )}
          </CardContent>
        </Card>

        <Card className="border-border bg-card">
          <CardHeader className="pb-3">
            <CardTitle className="text-sm">{t("insights.sources")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm text-muted-foreground">
            <div className="flex items-center gap-2">
              <Globe2 className="size-4" />
              <span>{t("insights.webSearch")}</span>
            </div>
            <div className="flex items-center gap-2">
              <Activity className="size-4" />
              <span>{t("insights.socketStream")}</span>
            </div>
          </CardContent>
        </Card>

        <Card className="border-border bg-card">
          <CardContent className="space-y-3 p-3 text-xs text-muted-foreground">
            <div className="flex items-center justify-between">
              <span>{t("insights.rate")}</span>
              <span>20 tokens/s</span>
            </div>
            <div className="flex items-center justify-between">
              <span>{t("insights.context")}</span>
              <span>44% used</span>
            </div>
            <Progress value={44} />
          </CardContent>
        </Card>
      </div>
    </ScrollArea>
  );
}

function Composer({
  t,
  project,
  value,
  disabled,
  submitting,
  onChange,
  onSend,
}: {
  t: ReturnType<typeof createTranslator>;
  project?: ProjectSummary;
  value: string;
  disabled: boolean;
  submitting: boolean;
  onChange: (value: string) => void;
  onSend: () => void;
}) {
  return (
    <div className="border-t border-border bg-[#181a1e] px-3 py-3 sm:px-5">
      <div className="mx-auto max-w-3xl rounded-lg border border-border bg-card shadow-panel">
        <Textarea
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={t("composer.placeholder")}
          disabled={disabled}
          className="min-h-[76px] resize-none border-0 bg-transparent focus-visible:ring-0"
          onKeyDown={(event) => {
            if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
              event.preventDefault();
              onSend();
            }
          }}
        />
        <div className="flex flex-col gap-2 border-t border-border px-2 py-2 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-1">
            <TooltipIcon label={t("composer.add")} icon={Plus} />
            <TooltipIcon label={t("composer.autoReview")} icon={ShieldCheck} />
            <Button variant="ghost" size="sm" className="h-8 gap-1 text-primary">
              <Moon className="size-4" />
              {t("composer.mode")}
              <ChevronDown className="size-3" />
            </Button>
            <Button variant="ghost" size="sm" className="hidden h-8 gap-1 text-muted-foreground sm:inline-flex">
              <Circle className="size-3" />
              {t("composer.target")}
            </Button>
          </div>
          <div className="flex items-center justify-between gap-2 sm:justify-end">
            <Badge variant="secondary" className="h-7 rounded-sm px-2 font-normal">
              {project?.name || "dash.ovload.com"}
            </Badge>
            <Badge variant="secondary" className="h-7 rounded-sm px-2 font-normal">
              5.5
            </Badge>
            <Button size="icon" disabled={disabled || submitting || !value.trim()} onClick={onSend} aria-label={t("composer.send")}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : <SendHorizonal className="size-4" />}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

function EventBlock({ event, t }: { event: RunEventRecord; t: ReturnType<typeof createTranslator> }) {
  const tone = eventTone(event.kind);
  const text = eventText(event, t);

  return (
    <article className="group">
      <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
        <EventIcon kind={event.kind} />
        <span>{eventKindLabel(t, event.kind)}</span>
        <span>{formatRelativeTime(event.event_at || event.received_at, t)}</span>
      </div>
      <div
        className={cn(
          "rounded-lg border px-4 py-3 text-sm leading-6",
          tone === "assistant" && "border-transparent bg-transparent px-0 text-[15px]",
          tone === "tool" && "border-border bg-card",
          tone === "terminal" && "border-border bg-[#0c0f11] font-mono text-xs",
          tone === "state" && "border-border bg-muted/45 text-muted-foreground",
          tone === "danger" && "border-destructive/40 bg-destructive/10 text-destructive",
        )}
      >
        {text}
      </div>
    </article>
  );
}

function WorkspacePane({
  t,
  agent,
  project,
}: {
  t: ReturnType<typeof createTranslator>;
  agent?: AgentSummary;
  project?: ProjectSummary;
}) {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<WorkspaceEntry[]>([]);
  const [selectedFile, setSelectedFile] = useState<WorkspaceFile | null>(null);
  const [git, setGit] = useState<WorkspaceGit | null>(null);
  const [loading, setLoading] = useState(false);
  const [fileLoading, setFileLoading] = useState(false);
  const [error, setError] = useState("");

  const canLoad = Boolean(agent?.id && project?.id);

  const refreshWorkspace = async (nextPath = path) => {
    if (!agent?.id || !project?.id) return;
    setLoading(true);
    setError("");
    try {
      const [tree, gitStatus] = await Promise.all([
        loadWorkspaceTree(agent.id, project.id, nextPath),
        loadWorkspaceGitStatus(agent.id, project.id).catch(() => null),
      ]);
      setEntries(tree.entries ?? []);
      setGit(gitStatus?.git ?? null);
      setPath(nextPath);
      if (selectedFile && !selectedFile.path.startsWith(nextPath)) {
        setSelectedFile(null);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t("workspace.loadFailed"));
      setEntries([]);
    } finally {
      setLoading(false);
    }
  };

  const openEntry = async (entry: WorkspaceEntry) => {
    if (!agent?.id || !project?.id) return;
    if (entry.kind === "directory") {
      setSelectedFile(null);
      await refreshWorkspace(entry.path);
      return;
    }
    setFileLoading(true);
    setError("");
    try {
      const response = await loadWorkspaceFile(agent.id, project.id, entry.path);
      setSelectedFile(response.file);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("workspace.loadFailed"));
    } finally {
      setFileLoading(false);
    }
  };

  useEffect(() => {
    setPath("");
    setSelectedFile(null);
    setEntries([]);
    setGit(null);
  }, [agent?.id, project?.id]);

  useEffect(() => {
    if (canLoad) {
      void refreshWorkspace("");
    }
  }, [agent?.id, project?.id]);

  const parentPath = path.split("/").slice(0, -1).join("/");
  const changesByPath = new Map((git?.files ?? []).map((change) => [change.path, change]));

  return (
    <ScrollArea className="h-full thin-scrollbar">
      <div className="mx-auto max-w-6xl p-4 sm:p-6">
        <div className="mb-5 flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 className="truncate text-lg font-semibold">{project?.name || t("workspace.title")}</h2>
            <p className="truncate text-sm text-muted-foreground">{project?.path || t("workspace.pending")}</p>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="secondary" size="sm" disabled={!canLoad || loading} onClick={() => void refreshWorkspace(path)}>
              {loading ? <Loader2 className="mr-2 size-4 animate-spin" /> : <RefreshCw className="mr-2 size-4" />}
              {t("workspace.refresh")}
            </Button>
            <Button variant="secondary" size="sm">
              <GitBranch className="mr-2 size-4" />
              {git?.branch || project?.git_branch || t("workspace.branch")}
            </Button>
          </div>
        </div>

        {error ? <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div> : null}

        <div className="grid min-h-[520px] gap-3 lg:grid-cols-[340px_minmax(0,1fr)]">
          <div className="min-h-0 rounded-md border border-border bg-card">
            <div className="flex h-10 items-center justify-between border-b border-border px-3 text-xs text-muted-foreground">
              <span className="truncate">{path || t("workspace.root")}</span>
              {path ? (
                <Button variant="ghost" size="icon" className="size-7" onClick={() => void refreshWorkspace(parentPath)} aria-label={t("workspace.up")}>
                  <ArrowUp className="size-4" />
                </Button>
              ) : null}
            </div>
            <div className="max-h-[540px] overflow-auto thin-scrollbar p-1">
              {entries.map((entry) => {
                const change = changesByPath.get(entry.path);
                const isSelected = selectedFile?.path === entry.path;
                return (
                  <button
                    key={entry.path}
                    type="button"
                    onClick={() => void openEntry(entry)}
                    className={cn(
                      "flex min-h-9 w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition hover:bg-accent",
                      isSelected && "bg-accent text-accent-foreground",
                    )}
                  >
                    {entry.kind === "directory" ? <Folder className="size-4 shrink-0 text-primary" /> : <FileText className="size-4 shrink-0 text-muted-foreground" />}
                    <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                    {entry.sensitive ? <Badge variant="secondary" className="h-5 rounded-sm px-1.5">{t("workspace.sensitive")}</Badge> : null}
                    {change ? <span className="text-xs text-success">{change.kind}</span> : null}
                  </button>
                );
              })}
              {!loading && entries.length === 0 ? <div className="p-6 text-center text-sm text-muted-foreground">{t("workspace.empty")}</div> : null}
              {loading ? <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t("workspace.loading")}</div> : null}
            </div>
          </div>

          <div className="min-w-0 rounded-md border border-border bg-card">
            <div className="flex h-10 items-center justify-between border-b border-border px-3">
              <div className="min-w-0 truncate text-sm font-medium">{selectedFile?.path || t("workspace.preview")}</div>
              {selectedFile ? <span className="text-xs text-muted-foreground">{formatBytes(selectedFile.size)}</span> : null}
            </div>
            <div className="min-h-[480px]">
              {fileLoading ? (
                <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t("workspace.loading")}</div>
              ) : selectedFile ? (
                selectedFile.binary ? (
                  <div className="p-6 text-sm text-muted-foreground">{t("workspace.binary")}</div>
                ) : (
                  <pre className="max-h-[540px] overflow-auto whitespace-pre-wrap p-4 font-mono text-xs leading-5 thin-scrollbar">
                    {selectedFile.content || ""}
                  </pre>
                )
              ) : (
                <div className="flex min-h-[480px] items-center justify-center p-6 text-sm text-muted-foreground">
                  <div className="text-center">
                    <Files className="mx-auto mb-3 size-8" />
                    <div>{t("workspace.selectFile")}</div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </ScrollArea>
  );
}

function ActivityPane({ t, events }: { t: ReturnType<typeof createTranslator>; events: RunEventRecord[] }) {
  return (
    <ScrollArea className="h-full thin-scrollbar">
      <div className="mx-auto max-w-4xl p-4 sm:p-6">
        <div className="space-y-2">
          {events.map((event) => (
            <div key={event.id || `${event.run_id}-${event.seq}`} className="flex items-start gap-3 rounded-md border border-border bg-card px-3 py-2">
              <EventIcon kind={event.kind} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center justify-between gap-3">
                  <span className="truncate text-sm font-medium">{eventKindLabel(t, event.kind)}</span>
                  <span className="text-xs text-muted-foreground">#{event.seq}</span>
                </div>
                <p className="mt-1 truncate text-xs text-muted-foreground">{eventText(event, t)}</p>
              </div>
            </div>
          ))}
          {events.length === 0 ? <EmptyConversation t={t} compact /> : null}
        </div>
      </div>
    </ScrollArea>
  );
}

function RunPicker({
  t,
  runs,
  activeRunId,
  onRunChange,
}: {
  t: ReturnType<typeof createTranslator>;
  runs: RunSummary[];
  activeRunId: string;
  onRunChange: (id: string) => void;
}) {
  const activeRun = runs.find((run) => run.id === activeRunId) ?? runs[0];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="secondary" size="sm" className="max-w-[180px] gap-2">
          <Play className="size-4" />
          <span className="truncate">{activeRun?.id || t("runs.none")}</span>
          <ChevronDown className="size-3" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuLabel>{t("runs.title")}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {runs.map((run) => (
          <DropdownMenuItem key={run.id} onClick={() => onRunChange(run.id)}>
            <span className="min-w-0 flex-1 truncate">{run.id}</span>
            <Badge variant={run.status === "done" ? "secondary" : "default"}>{runStatusLabel(t, run.status)}</Badge>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function LanguageMenu({ locale, onLocaleChange }: { locale: Locale; onLocaleChange: (locale: Locale) => void }) {
  return (
    <Select value={locale} onValueChange={(value) => onLocaleChange(value as Locale)}>
      <SelectTrigger className="h-8 w-[112px] border-0 bg-transparent px-2">
        <Globe2 className="mr-2 size-4" />
        <SelectValue />
      </SelectTrigger>
      <SelectContent align="end">
        {(Object.keys(localeNames) as Locale[]).map((nextLocale) => (
          <SelectItem key={nextLocale} value={nextLocale}>
            {localeNames[nextLocale]}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function NavAction({ icon: Icon, label, active = false }: { icon: ElementType; label: string; active?: boolean }) {
  return (
    <button
      type="button"
      className={cn(
        "flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm text-muted-foreground transition hover:bg-accent hover:text-accent-foreground",
        active && "bg-accent text-accent-foreground",
      )}
    >
      <Icon className="size-4 shrink-0" />
      <span className="truncate">{label}</span>
    </button>
  );
}

function SidebarRow({
  icon: Icon,
  label,
  meta,
  active,
  status,
  onClick,
}: {
  icon: ElementType;
  label: string;
  meta?: string;
  active?: boolean;
  status?: "good" | "warn" | "muted";
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex min-h-8 w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-muted-foreground transition hover:bg-accent hover:text-accent-foreground",
        active && "bg-accent text-accent-foreground",
      )}
    >
      <Icon className="size-4 shrink-0" />
      <span className="min-w-0 flex-1 truncate font-medium">{label}</span>
      {meta ? <span className="max-w-[72px] truncate text-xs opacity-70">{meta}</span> : null}
      {status ? (
        <span
          className={cn(
            "size-2 shrink-0 rounded-full",
            status === "good" && "bg-success",
            status === "warn" && "bg-warning",
            status === "muted" && "bg-muted-foreground",
          )}
        />
      ) : null}
    </button>
  );
}

function SectionLabel({ children }: { children: ReactNode }) {
  return <div className="mb-2 px-2 text-xs font-medium uppercase tracking-normal text-muted-foreground">{children}</div>;
}

function InsightRow({
  icon: Icon,
  label,
  value,
  valueClassName,
}: {
  icon: ElementType;
  label: string;
  value: string;
  valueClassName?: string;
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="flex min-w-0 items-center gap-2 text-muted-foreground">
        <Icon className="size-4 shrink-0" />
        <span className="truncate">{label}</span>
      </div>
      <span className={cn("shrink-0 text-right font-medium", valueClassName)}>{value}</span>
    </div>
  );
}

function ApprovalItem({ t, approvalId }: { t: ReturnType<typeof createTranslator>; approvalId: string }) {
  return (
    <div className="rounded-md border border-border bg-muted/30 p-2 text-sm">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="truncate font-medium">{approvalId}</span>
        <Bell className="size-4 text-warning" />
      </div>
      <div className="flex gap-2">
        <Button size="sm" className="h-7 flex-1" disabled>
          {t("approvals.approve")}
        </Button>
        <Button size="sm" variant="secondary" className="h-7 flex-1" disabled>
          {t("approvals.reject")}
        </Button>
      </div>
    </div>
  );
}

function TooltipIcon({ icon: Icon, label }: { icon: ElementType; label: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant="ghost" size="icon" className="size-8" aria-label={label}>
          <Icon className="size-4" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function EventIcon({ kind }: { kind: string }) {
  if (kind.includes("tool")) return <Terminal className="mt-0.5 size-4 shrink-0 text-primary" />;
  if (kind.includes("diff")) return <GitBranch className="mt-0.5 size-4 shrink-0 text-success" />;
  if (kind.includes("error") || kind.includes("failed")) return <X className="mt-0.5 size-4 shrink-0 text-destructive" />;
  if (kind.includes("done") || kind.includes("completed")) return <Check className="mt-0.5 size-4 shrink-0 text-success" />;
  if (kind.includes("terminal")) return <Terminal className="mt-0.5 size-4 shrink-0 text-warning" />;
  return <Bot className="mt-0.5 size-4 shrink-0 text-muted-foreground" />;
}

function LoadingConversation({ t }: { t: ReturnType<typeof createTranslator> }) {
  return (
    <div className="space-y-4">
      <div className="h-4 w-44 animate-pulse rounded bg-muted" />
      <div className="h-24 animate-pulse rounded-lg bg-muted" />
      <div className="h-4 w-64 animate-pulse rounded bg-muted" />
    </div>
  );
}

function EmptyConversation({ t, compact = false }: { t: ReturnType<typeof createTranslator>; compact?: boolean }) {
  return (
    <div className={cn("flex flex-col items-center justify-center rounded-lg border border-dashed border-border text-center", compact ? "p-6" : "min-h-[320px] p-10")}>
      <Avatar className="mb-3 size-10">
        <AvatarFallback>
          <Command className="size-5" />
        </AvatarFallback>
      </Avatar>
      <h2 className="text-base font-semibold">{t("conversation.emptyTitle")}</h2>
      <p className="mt-2 max-w-sm text-sm text-muted-foreground">{t("conversation.emptyText")}</p>
    </div>
  );
}

function agentStatusLabel(t: ReturnType<typeof createTranslator>, agent: AgentSummary) {
  if (agent.disabled) return t("status.disabled");
  if (agent.status === "online") return t("status.online");
  if (agent.status === "running") return t("status.running");
  return t("status.idle");
}

function runStatusLabel(t: ReturnType<typeof createTranslator>, status?: string) {
  switch (status) {
    case "running":
      return t("runs.status.running");
    case "done":
    case "completed":
      return t("runs.status.done");
    case "error":
    case "failed":
      return t("runs.status.failed");
    case "stopped":
      return t("runs.status.stopped");
    default:
      return t("runs.status.pending");
  }
}

function eventKindLabel(t: ReturnType<typeof createTranslator>, kind: string) {
  if (kind.startsWith("assistant.message")) return t("events.assistant");
  if (kind.startsWith("tool.")) return t("events.tool");
  if (kind.startsWith("terminal.")) return t("events.terminal");
  if (kind.startsWith("diff.")) return t("events.diff");
  if (kind.startsWith("approval.")) return t("events.approval");
  if (kind.startsWith("run.")) return t("events.run");
  return kind;
}

function eventText(event: RunEventRecord, t: ReturnType<typeof createTranslator>) {
  const payload: unknown = event.payload ?? {};
  if (typeof payload === "string") return payload;
  if (isRecord(payload)) {
    const text = payload.text ?? payload.message ?? payload.delta ?? payload.output ?? payload.summary ?? payload.status;
    if (typeof text === "string" && text.trim()) return text;
    const command = payload.command ?? payload.name ?? payload.path;
    if (typeof command === "string" && command.trim()) return command;
  }
  return t("events.noPayload");
}

function eventTone(kind: string) {
  if (kind.includes("error") || kind.includes("failed")) return "danger";
  if (kind.includes("tool")) return "tool";
  if (kind.includes("terminal")) return "terminal";
  if (kind.includes("run.") || kind.includes("session.") || kind.includes("approval.")) return "state";
  return "assistant";
}

function formatDiffStat(model: DashboardModel | null) {
  const changes = model?.commands.length ?? 0;
  return `+${changes} -0`;
}

function getAgentHealth(agent?: AgentSummary) {
  if (!agent) return "n/a";
  if (agent.disabled) return "disabled";
  if (isRecord(agent.health) && typeof agent.health.status === "string") return agent.health.status;
  return agent.status || "idle";
}

function formatRelativeTime(value: string | undefined, t: ReturnType<typeof createTranslator>) {
  if (!value) return "";
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return "";
  const diff = Date.now() - timestamp;
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (diff < minute) return t("time.now");
  if (diff < hour) return t("time.minutes", { count: Math.max(1, Math.floor(diff / minute)) });
  if (diff < day) return t("time.hours", { count: Math.max(1, Math.floor(diff / hour)) });
  return t("time.days", { count: Math.max(1, Math.floor(diff / day)) });
}

function formatBytes(value: number | undefined): string {
  if (!value || value < 0) return "0 B";
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB"];
  let size = value / 1024;
  for (const unit of units) {
    if (size < 1024) return `${size.toFixed(size >= 10 ? 0 : 1)} ${unit}`;
    size /= 1024;
  }
  return `${size.toFixed(1)} TB`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function messagePayload(message: unknown): Record<string, unknown> | undefined {
  if (!isRecord(message) || !isRecord(message.payload)) {
    return undefined;
  }
  return message.payload;
}

function normalizeRunEvent(value: unknown, fallbackAgentId = ""): RunEventRecord | null {
  if (!isRecord(value)) {
    return null;
  }

  const runId = stringValue(value.run_id);
  const seq = numberValue(value.seq);
  const kind = stringValue(value.kind);
  if (!runId || !seq || !kind) {
    return null;
  }

  const at = stringValue(value.event_at) || stringValue(value.at) || new Date().toISOString();
  return {
    id: stringValue(value.id) || stringValue(value.event_id) || `${runId}-${seq}`,
    agent_id: stringValue(value.agent_id) || fallbackAgentId,
    device_id: stringValue(value.device_id),
    run_id: runId,
    command_id: stringValue(value.command_id),
    session_id: stringValue(value.session_id),
    project_id: stringValue(value.project_id),
    seq,
    kind,
    payload: isRecord(value.payload) ? (value.payload as JsonRecord) : undefined,
    event_at: at,
    received_at: stringValue(value.received_at) || at,
  };
}

function isRunEventRecord(value: RunEventRecord | null): value is RunEventRecord {
  return value !== null;
}

function mergeRunEvents(model: DashboardModel, incoming: RunEventRecord[]): DashboardModel {
  if (incoming.length === 0) {
    return model;
  }
  const events = new Map<string, RunEventRecord>();
  for (const event of model.run_events) {
    events.set(runEventKey(event), event);
  }
  for (const event of incoming) {
    events.set(runEventKey(event), event);
  }
  return {
    ...model,
    run_events: [...events.values()].sort((left, right) => left.seq - right.seq),
  };
}

function runEventKey(event: RunEventRecord): string {
  return event.id || `${event.run_id}:${event.seq}`;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberValue(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

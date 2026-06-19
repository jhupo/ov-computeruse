import { getDashToken } from "./session";
import type { DashWsClientMessage, DashWsServerMessage } from "./types";

export interface DashSocketOptions {
  baseUrl?: string;
  token?: string | (() => string | null);
  protocols?: string | string[];
}

export type DashSocketListener = (message: DashWsServerMessage) => void;

export class DashSocket {
  private readonly options: DashSocketOptions;
  private socket: WebSocket | null = null;
  private readonly listeners = new Set<DashSocketListener>();

  constructor(options: DashSocketOptions = {}) {
    this.options = options;
  }

  connect(): WebSocket {
    if (this.socket && this.socket.readyState <= WebSocket.OPEN) {
      return this.socket;
    }

    this.socket = new WebSocket(this.url(), this.options.protocols);
    this.socket.addEventListener("message", (event) => {
      const message = decodeMessage(event.data);
      if (message) {
        this.listeners.forEach((listener) => listener(message));
      }
    });
    return this.socket;
  }

  close(code?: number, reason?: string): void {
    this.socket?.close(code, reason);
    this.socket = null;
  }

  send(message: DashWsClientMessage): void {
    const socket = this.connect();
    const payload = JSON.stringify(message);
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(payload);
      return;
    }
    socket.addEventListener("open", () => socket.send(payload), { once: true });
  }

  subscribe(listener: DashSocketListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  subscribeRun(agent_id: string, run_id: string, after_seq = 0, limit = 300): void {
    this.send({ type: "run.subscribe", agent_id, run_id, after_seq, limit });
  }

  unsubscribeRun(agent_id: string, run_id: string): void {
    this.send({ type: "run.unsubscribe", agent_id, run_id });
  }

  ping(): void {
    this.send({ type: "ping" });
  }

  private url(): string {
    const base = this.options.baseUrl ?? importMetaEnv("VITE_DASH_WS_BASE_URL") ?? browserWsOrigin();
    const url = new URL("/ws/dash", base);
    const token = this.resolveToken();
    if (token) {
      url.searchParams.set("token", token);
    }
    return url.toString();
  }

  private resolveToken(): string | null {
    if (typeof this.options.token === "function") {
      return this.options.token();
    }
    return this.options.token ?? getDashToken();
  }
}

export function createDashSocket(options: DashSocketOptions = {}): DashSocket {
  return new DashSocket(options);
}

function decodeMessage(data: unknown): DashWsServerMessage | null {
  if (typeof data !== "string") {
    return null;
  }
  try {
    return JSON.parse(data) as DashWsServerMessage;
  } catch {
    return null;
  }
}

function browserWsOrigin(): string {
  if (typeof window === "undefined") {
    return "ws://localhost";
  }
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}`;
}

function importMetaEnv(key: string): string | undefined {
  const meta = import.meta as ImportMeta & { env?: Record<string, string | undefined> };
  return meta.env?.[key];
}

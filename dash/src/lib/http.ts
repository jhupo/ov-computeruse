import { getDashToken } from "./session";
import type { ApiErrorBody } from "./types";

export interface DashHttpClientOptions {
  baseUrl?: string;
  token?: string | (() => string | null);
  fetcher?: typeof fetch;
  mockFallback?: boolean;
}

export class DashApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: unknown;

  constructor(status: number, code: string, message: string, body?: unknown) {
    super(message);
    this.name = "DashApiError";
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

export class DashHttpClient {
  readonly baseUrl: string;
  readonly mockFallback: boolean;
  private readonly token?: string | (() => string | null);
  private readonly fetcher: typeof fetch;

  constructor(options: DashHttpClientOptions = {}) {
    this.baseUrl = trimTrailingSlash(options.baseUrl ?? importMetaEnv("VITE_DASH_API_BASE_URL") ?? "");
    this.token = options.token;
    this.fetcher = options.fetcher ?? fetch.bind(globalThis);
    this.mockFallback = options.mockFallback ?? importMetaEnv("VITE_DASH_MOCK_FALLBACK") !== "false";
  }

  get<T>(path: string, query?: Record<string, string | number | boolean | null | undefined>): Promise<T> {
    return this.request<T>("GET", path, undefined, query);
  }

  post<T>(path: string, body?: unknown, query?: Record<string, string | number | boolean | null | undefined>): Promise<T> {
    return this.request<T>("POST", path, body, query);
  }

  async request<T>(
    method: string,
    path: string,
    body?: unknown,
    query?: Record<string, string | number | boolean | null | undefined>,
  ): Promise<T> {
    const response = await this.fetcher(this.url(path, query), {
      method,
      headers: this.headers(body),
      body: body === undefined ? undefined : JSON.stringify(body),
    });

    if (!response.ok) {
      throw await toApiError(response);
    }

    if (response.status === 204) {
      return undefined as T;
    }

    return (await response.json()) as T;
  }

  url(path: string, query?: Record<string, string | number | boolean | null | undefined>): string {
    const pathname = path.startsWith("/") ? path : `/${path}`;
    const url = new URL(`${this.baseUrl}${pathname}`, browserOrigin());

    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined && value !== null && value !== "") {
        url.searchParams.set(key, String(value));
      }
    }

    return url.toString();
  }

  private headers(body: unknown): Headers {
    const headers = new Headers();
    if (body !== undefined) {
      headers.set("content-type", "application/json");
    }

    const token = this.resolveToken();
    if (token) {
      headers.set("authorization", `Bearer ${token}`);
    }

    return headers;
  }

  private resolveToken(): string | null {
    if (typeof this.token === "function") {
      return this.token();
    }
    return this.token ?? getDashToken();
  }
}

async function toApiError(response: Response): Promise<DashApiError> {
  const body = await readErrorBody(response);
  const error = normalizeErrorBody(body);
  return new DashApiError(response.status, error.code, error.message, body);
}

async function readErrorBody(response: Response): Promise<unknown> {
  const contentType = response.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    return response.json().catch(() => undefined);
  }
  return response.text().catch(() => undefined);
}

function normalizeErrorBody(body: unknown): { code: string; message: string } {
  if (isApiErrorBody(body)) {
    if (body.error) {
      return body.error;
    }
    return {
      code: body.code ?? "request_failed",
      message: body.message ?? "Request failed",
    };
  }
  return { code: "request_failed", message: "Request failed" };
}

function isApiErrorBody(value: unknown): value is ApiErrorBody {
  return typeof value === "object" && value !== null;
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "");
}

function browserOrigin(): string {
  if (typeof window !== "undefined") {
    return window.location.origin;
  }
  return "http://localhost";
}

function importMetaEnv(key: string): string | undefined {
  const meta = import.meta as ImportMeta & { env?: Record<string, string | undefined> };
  return meta.env?.[key];
}

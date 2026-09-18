/**
 * Go 后端访问层（与 personal-admin 的 api-client 同构，本地版去掉全部认证逻辑）。
 * - 统一信封：成功 `{code:"OK", message, data, requestId}`，失败 `{code, message, details?, requestId}`；
 * - API 基址：dev 模式走 vite 代理（相对路径）；Wails 打包后通过 Go 绑定发现实际端口。
 */

export interface ApiErrorDetail {
  field: string;
  reason: string;
}

export interface ApiErrorPayload {
  message?: string;
  code?: string;
  requestId?: string;
  details?: ApiErrorDetail[];
}

export class ApiError extends Error {
  status: number;
  code?: string;
  requestId?: string;
  details?: ApiErrorDetail[];

  constructor(status: number, payload: ApiErrorPayload) {
    super(payload.message || "请求失败");
    this.name = "ApiError";
    this.status = status;
    this.code = payload.code;
    this.requestId = payload.requestId;
    this.details = payload.details;
  }
}

interface Envelope<T> {
  code: string;
  message: string;
  data?: T;
  requestId?: string;
}

let apiBase = "";

/**
 * 发现 API 基址。Wails 环境从 Go 绑定读实际端口（默认 37421 被占用时会同向顺延）；
 * 取不到绑定（vite dev）时保持空串走相对路径（vite proxy）。
 */
export async function initApiBase(): Promise<void> {
  if (import.meta.env.VITE_API_BASE_URL) {
    apiBase = String(import.meta.env.VITE_API_BASE_URL);
    return;
  }
  try {
    const bindings = (window as unknown as { go?: { opsbox?: { App?: { ServerPort?: () => Promise<number> } } } }).go;
    const port = await bindings?.opsbox?.App?.ServerPort?.();
    if (port && port > 0) {
      apiBase = `http://127.0.0.1:${port}`;
    }
  } catch {
    // 非 Wails 环境：走相对路径
  }
}

export function getApiBase(): string {
  return apiBase;
}

async function parseResponse<T>(response: Response): Promise<T> {
  if (response.status === 204) return undefined as T;
  const payload = (await response.json().catch(() => null)) as Envelope<T> | null;
  if (!response.ok) {
    throw new ApiError(
      response.status,
      payload ?? { code: "UNKNOWN", message: "请求失败" },
    );
  }
  return payload!.data as T;
}

export async function apiRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, {
    ...init,
    headers: new Headers({
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    }),
  });
  return parseResponse<T>(response);
}

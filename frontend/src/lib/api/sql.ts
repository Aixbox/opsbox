import { apiRequest, getApiBase } from "../api-client";
import { buildQuery } from "./types";

/** SQL AI 运维模块（契约见 backend/api/openapi.yaml，SQL tag） */

export type SqlEngine = "mysql" | "postgres";
export type WritePolicy = "readonly" | "confirm" | "allow";
export type QueryKind = "read" | "write" | "schema";
export type QueryStatus = "pending" | "running" | "success" | "failed" | "timeout" | "blocked" | "rejected";

export interface SqlConnection {
  id: number;
  name: string;
  engine: SqlEngine;
  host: string;
  port: number;
  username: string;
  hasPassword: boolean;
  database: string;
  params: string;
  writePolicy: WritePolicy;
  enabled: boolean;
  remark: string;
  createdAt: string;
  updatedAt: string;
}

/** 密码字段：不传（undefined）保持不变，空串清空（PostgreSQL 免密场景） */
export interface SqlConnectionInput {
  name: string;
  engine: SqlEngine;
  host: string;
  port: number;
  username: string;
  password?: string;
  database: string;
  params: string;
  writePolicy: WritePolicy;
  enabled: boolean;
  remark: string;
}

export interface SqlSettings {
  maxRows: number;
  queryTimeoutSeconds: number;
  blockedPatterns: string[];
  /** 查询结果加密保存天数；0 = 只留元数据 */
  outputRetentionDays: number;
  updatedAt?: string;
}

export interface SqlLogOptions {
  tx?: boolean;
  timeoutSeconds?: number;
  maxRows?: number;
}

export interface SqlQueryLog {
  id: number;
  connectionId: number;
  connectionName: string;
  userId: number | null;
  username: string;
  kind: QueryKind;
  statements: string;
  status: QueryStatus;
  rowsReturned: number;
  rowsAffected: number;
  truncated: boolean;
  durationMs: number;
  error: string;
  options: SqlLogOptions;
  approvedBy: number | null;
  approvedByName: string;
  approvedAt: string | null;
  createdAt: string;
  finishedAt: string | null;
}

export interface SqlQueryOutcome {
  id: number;
  status: QueryStatus;
  kind: QueryKind;
  columns?: string[];
  rows?: unknown[][];
  rowsReturned: number;
  rowsAffected: number;
  truncated: boolean;
  durationMs: number;
  error?: string;
  blockedRule?: string;
  blockedReason?: string;
  /** 查询已完成但结果已过保存期（或设置为不保存），只剩元数据 */
  outputExpired?: boolean;
}

export interface SqlLogPage {
  items: SqlQueryLog[];
  total: number;
  page: number;
  pageSize: number;
}

export interface SqlLogQuery {
  page?: number;
  pageSize?: number;
  connectionId?: number;
  userId?: number;
  status?: QueryStatus | "";
  kind?: QueryKind | "";
  from?: string;
  to?: string;
}

export interface SqlQueryInput {
  sql: string;
  tx?: boolean;
  timeoutSeconds?: number;
}

export interface SqlSessionInfo {
  sessionId: string;
  ticket: string;
}

/** 已打开的控制台会话：会话即授权，列表里没有的连接 CLI 无法操作。会话是持久的，关掉面板不结束 */
export interface SqlSessionSummary {
  sessionId: string;
  /** 进程内递增序号，没起名时界面显示「会话 #N」 */
  seq: number;
  /** 用户起的名字；空串表示没起过 */
  name: string;
  connectionId: number;
  connectionName: string;
  attached: number;
  createdAt: string;
  lastSeenAt: string;
}

/** 控制台事件：谁、执行了什么、结果如何 */
export interface SqlConsoleEvent {
  type: string;
  source: "cli" | "web" | "approved";
  logId?: number;
  command?: string;
  kind?: QueryKind;
  status?: string;
  columns?: string[];
  rows?: unknown[][];
  error?: string;
  note?: string;
  durationMs?: number;
  at: string;
}

const base = "/api/v1/sql";
const get = <T>(path: string, params: object = {}, signal?: AbortSignal) =>
  apiRequest<T>(`${base}${path}?${buildQuery(params)}`, { signal });
const write = <T>(path: string, method: string, body?: unknown) =>
  apiRequest<T>(`${base}${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) });

export const sqlApi = {
  connections: (signal?: AbortSignal) => get<{ items: SqlConnection[] }>("/connections", {}, signal),
  saveConnection: (input: SqlConnectionInput, id?: number) =>
    write<SqlConnection>(id ? `/connections/${id}` : "/connections", id ? "PUT" : "POST", input),
  deleteConnection: (id: number) => write<void>(`/connections/${id}`, "DELETE"),
  testConnection: (id: number) =>
    write<{ ok: boolean; engine: string; database: string }>(`/connections/${id}/test`, "POST"),
  /** 按表单参数测试连通性（添加 / 编辑抽屉的「测试连接」）；fromId= 编辑场景下密码留空回退已保存值 */
  testTarget: (input: SqlConnectionInput, fromId?: number) =>
    write<{ ok: boolean; engine: string; database: string }>(
      `/connections/test?${buildQuery({ fromId })}`,
      "POST",
      input,
    ),
  query: (id: number, input: SqlQueryInput) => write<SqlQueryOutcome>(`/connections/${id}/query`, "POST", input),
  queryStatus: (logId: number, signal?: AbortSignal) => get<SqlQueryOutcome>(`/queries/${logId}`, {}, signal),
  pending: (signal?: AbortSignal) => get<{ items: SqlQueryLog[] }>("/pending-queries", {}, signal),
  approve: (id: number) => write<SqlQueryLog>(`/queries/${id}/approve`, "POST"),
  reject: (id: number) => write<SqlQueryLog>(`/queries/${id}/reject`, "POST"),
  logs: (params: SqlLogQuery, signal?: AbortSignal) => get<SqlLogPage>("/query-logs", params, signal),
  settings: (signal?: AbortSignal) => get<SqlSettings>("/settings", {}, signal),
  saveSettings: (input: SqlSettings) => write<SqlSettings>("/settings", "PUT", input),
  /** 打开控制台会话（会话即授权，CLI 见到它才会放行该连接） */
  openSession: (id: number) => write<SqlSessionInfo>(`/connections/${id}/sessions`, "POST", {}),
  sessions: (id: number, signal?: AbortSignal) =>
    get<{ items: SqlSessionSummary[] }>(`/connections/${id}/sessions`, {}, signal),
  /** 为已有会话再签一张一次性票据：重新打开面板 / 断线重连时接回持久会话 */
  issueTicket: (id: number, sid: string) =>
    write<{ ticket: string }>(`/connections/${id}/sessions/${sid}/ticket`, "POST"),
  /** 主动结束会话（关闭面板不会结束会话） */
  closeSession: (id: number, sid: string) => write<void>(`/connections/${id}/sessions/${sid}`, "DELETE"),
  /** 给会话起名（标签页上显示）；空串恢复默认的「会话 #N」 */
  renameSession: (id: number, sid: string, name: string) =>
    write<{ name: string }>(`/connections/${id}/sessions/${sid}/rename`, "POST", { name }),
  /** WebSocket 地址：直连本地 API 端口（Wails 打包后经 getApiBase 拿到实际端口；dev 由 vite proxy 转发） */
  consoleSocketUrl: (id: number, sid: string, ticket: string) => {
    // 不能用 window.location.origin 兜底：Wails 打包后页面跑在 wails.localhost 资源服务上，
    // 它不代理 /api/v1，WebSocket 握手必失败（表现为「连接已断开」）；dev 下 getApiBase 为空串走 vite proxy
    const origin = getApiBase() || window.location.origin;
    const url = new URL(`${base}/connections/${id}/sessions/${sid}/ws`, origin);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("ticket", ticket);
    return url.toString();
  },
};

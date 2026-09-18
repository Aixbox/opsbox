import { apiRequest } from "../api-client";
import { buildQuery } from "./types";

/** Redis AI 运维模块（契约见 backend/api/openapi.yaml，Redis tag） */

export type WritePolicy = "readonly" | "confirm" | "allow";
export type LogStatus = "pending" | "running" | "success" | "failed" | "timeout" | "blocked" | "rejected";

export interface RedisConnection {
  id: number;
  name: string;
  host: string;
  port: number;
  hasPassword: boolean;
  db: number;
  tls: boolean;
  writePolicy: WritePolicy;
  enabled: boolean;
  remark: string;
  createdAt: string;
  updatedAt: string;
}

/** 密码字段：不传（undefined）保持不变，空串清空 */
export interface RedisConnectionInput {
  name: string;
  host: string;
  port: number;
  password?: string;
  db: number;
  tls: boolean;
  writePolicy: WritePolicy;
  enabled: boolean;
  remark: string;
}

export interface RedisSettings {
  scanKeyLimit: number;
  valueTruncateBytes: number;
  blockedCommands: string[];
  /** 命令回复加密保存天数；0 = 只留元数据 */
  outputRetentionDays: number;
  updatedAt?: string;
}

export interface RedisLogOptions {
  args?: string[];
  reason?: string;
}

export interface RedisExecLog {
  id: number;
  connectionId: number;
  connectionName: string;
  userId: number | null;
  username: string;
  command: string;
  status: LogStatus;
  replyBytes: number;
  truncated: boolean;
  durationMs: number;
  error: string;
  options: RedisLogOptions;
  approvedBy: number | null;
  approvedByName: string;
  approvedAt: string | null;
  createdAt: string;
  finishedAt: string | null;
}

export interface RedisExecOutcome {
  id: number;
  status: LogStatus;
  reply?: unknown;
  text?: string;
  replyBytes: number;
  truncated: boolean;
  durationMs: number;
  error?: string;
  reason?: string;
  replyNote?: string;
}

export interface RedisScanResult {
  keys: string[];
  truncated: boolean;
  total: number;
  durationMs: number;
}

export interface RedisLogPage {
  items: RedisExecLog[];
  total: number;
  page: number;
  pageSize: number;
}

export interface RedisLogQuery {
  page?: number;
  pageSize?: number;
  connectionId?: number;
  userId?: number;
  status?: LogStatus | "";
  from?: string;
  to?: string;
}

export interface RedisSessionInfo {
  sessionId: string;
  ticket: string;
}

/** 已打开的控制台会话：会话即授权，列表里没有的连接 CLI 无法操作。会话是持久的，关掉面板不结束 */
export interface RedisSessionSummary {
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
export interface RedisConsoleEvent {
  type: string;
  source: "cli" | "web" | "approved";
  logId?: number;
  command?: string;
  kind?: string;
  status?: LogStatus;
  reply?: unknown;
  text?: string;
  error?: string;
  note?: string;
  durationMs?: number;
  at: string;
}

const base = "/api/v1/redis";
const get = <T>(path: string, params: object = {}, signal?: AbortSignal) =>
  apiRequest<T>(`${base}${path}?${buildQuery(params)}`, { signal });
const write = <T>(path: string, method: string, body?: unknown) =>
  apiRequest<T>(`${base}${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) });

export const redisApi = {
  connections: (signal?: AbortSignal) => get<{ items: RedisConnection[] }>("/connections", {}, signal),
  saveConnection: (input: RedisConnectionInput, id?: number) =>
    write<RedisConnection>(id ? `/connections/${id}` : "/connections", id ? "PUT" : "POST", input),
  deleteConnection: (id: number) => write<void>(`/connections/${id}`, "DELETE"),
  testConnection: (id: number) =>
    write<{ ok: boolean; version: string; db: number }>(`/connections/${id}/test`, "POST"),
  pending: (signal?: AbortSignal) => get<{ items: RedisExecLog[] }>("/pending-commands", {}, signal),
  approve: (id: number) => write<RedisExecLog>(`/commands/${id}/approve`, "POST"),
  reject: (id: number) => write<RedisExecLog>(`/commands/${id}/reject`, "POST"),
  command: (id: number, signal?: AbortSignal) => get<RedisExecOutcome>(`/commands/${id}`, {}, signal),
  scan: (id: number, input: { pattern?: string; limit?: number; count?: number }) =>
    write<RedisScanResult>(`/connections/${id}/scan`, "POST", input),
  logs: (params: RedisLogQuery, signal?: AbortSignal) => get<RedisLogPage>("/command-logs", params, signal),
  settings: (signal?: AbortSignal) => get<RedisSettings>("/settings", {}, signal),
  saveSettings: (input: RedisSettings) => write<RedisSettings>("/settings", "PUT", input),
  exec: (id: number, input: { args: string[] }) => write<RedisExecOutcome>(`/connections/${id}/exec`, "POST", input),
  /** 打开控制台会话（会话即授权，CLI 见到它才会放行该连接） */
  openSession: (id: number) => write<RedisSessionInfo>(`/connections/${id}/sessions`, "POST", {}),
  sessions: (id: number, signal?: AbortSignal) =>
    get<{ items: RedisSessionSummary[] }>(`/connections/${id}/sessions`, {}, signal),
  /** 为已有会话再签一张一次性票据：重新打开面板 / 断线重连时接回持久会话 */
  issueTicket: (id: number, sid: string) =>
    write<{ ticket: string }>(`/connections/${id}/sessions/${sid}/ticket`, "POST"),
  /** 主动结束会话（关闭面板不会结束会话） */
  closeSession: (id: number, sid: string) => write<void>(`/connections/${id}/sessions/${sid}`, "DELETE"),
  /** 给会话起名（标签页上显示）；空串恢复默认的「会话 #N」 */
  renameSession: (id: number, sid: string, name: string) =>
    write<{ name: string }>(`/connections/${id}/sessions/${sid}/rename`, "POST", { name }),
  /** WebSocket 地址：同源代理（dev 由 vite proxy 转发 /api/v1）或 VITE_API_BASE_URL */
  consoleSocketUrl: (id: number, sid: string, ticket: string) => {
    const apiBase = import.meta.env.VITE_API_BASE_URL ?? "";
    const origin = apiBase || window.location.origin;
    const url = new URL(`${base}/connections/${id}/sessions/${sid}/ws`, origin);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("ticket", ticket);
    return url.toString();
  },
};

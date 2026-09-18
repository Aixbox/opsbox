import { apiRequest, getApiBase } from "../api-client";
import { buildQuery } from "./types";

/** SSH 运维模块（契约与 personal-admin backend/api/openapi.yaml SSH tag 一致） */

export type ExecPolicy = "audit" | "approve";
export type AuthType = "password" | "key";
export type LogKind = "exec" | "upload" | "download" | "session";
export type LogStatus = "pending" | "approved" | "running" | "success" | "failed" | "timeout" | "blocked" | "rejected";

export interface SshConnection {
  id: number;
  name: string;
  host: string;
  port: number;
  username: string;
  authType: AuthType;
  hasPassword: boolean;
  hasPrivateKey: boolean;
  hasPassphrase: boolean;
  hostKey: string;
  execPolicy: ExecPolicy;
  enabled: boolean;
  remark: string;
  createdAt: string;
  updatedAt: string;
}

/** 凭证字段：不传（undefined）保持不变，空串清空 */
export interface SshConnectionInput {
  name: string;
  host: string;
  port: number;
  username: string;
  authType: AuthType;
  password?: string;
  privateKey?: string;
  passphrase?: string;
  resetHostKey?: boolean;
  execPolicy: ExecPolicy;
  enabled: boolean;
  remark: string;
}

export interface SshSettings {
  blacklistPatterns: string[];
  execTimeoutSeconds: number;
  outputLimitBytes: number;
  dynamicRequiresApproval: boolean;
  /** 命令输出加密保存天数；0 = 只留元数据 */
  outputRetentionDays: number;
  updatedAt?: string;
}

export interface SshLogOptions {
  timeoutSeconds?: number;
  pty?: boolean;
  dynamicReason?: string;
  rows?: number;
  cols?: number;
  /** 传输类记录声明的文件总字节数 */
  totalBytes?: number;
  sessionId?: string;
}

export interface SshExecLog {
  id: number;
  connectionId: number;
  connectionName: string;
  userId: number | null;
  username: string;
  kind: LogKind;
  command: string;
  status: LogStatus;
  exitCode: number | null;
  stdoutBytes: number;
  stderrBytes: number;
  truncated: boolean;
  durationMs: number;
  error: string;
  options: SshLogOptions;
  approvedBy: number | null;
  approvedByName: string;
  approvedAt: string | null;
  createdAt: string;
  finishedAt: string | null;
}

export interface SshExecOutcome {
  id: number;
  status: LogStatus;
  exitCode?: number;
  stdout?: string;
  stderr?: string;
  stdoutBytes: number;
  stderrBytes: number;
  truncated: boolean;
  durationMs: number;
  error?: string;
  blockedRule?: string;
  blockedReason?: string;
  dynamic?: boolean;
  dynamicReason?: string;
  outputExpired?: boolean;
}

export interface SshSessionInfo {
  logId: number;
  status: "pending" | "running";
  sessionId?: string;
  ticket?: string;
}

/** 已打开的终端会话：会话即授权。会话是持久的，关掉面板不结束 */
export interface SshSessionSummary {
  sessionId: string;
  /** 进程内递增序号，没起名时界面显示「会话 #N」 */
  seq: number;
  /** 用户起的名字；空串表示没起过 */
  name: string;
  connectionId: number;
  connectionName: string;
  logId: number;
  attached: number;
  createdAt: string;
  lastSeenAt: string;
}

export interface SshLogPage {
  items: SshExecLog[];
  total: number;
  page: number;
  pageSize: number;
}

export interface SshLogQuery {
  page?: number;
  pageSize?: number;
  connectionId?: number;
  userId?: number;
  status?: LogStatus | "";
  kind?: LogKind | "";
  from?: string;
  to?: string;
}

const base = "/api/v1/ssh";
const get = <T>(path: string, params: object = {}, signal?: AbortSignal) =>
  apiRequest<T>(`${base}${path}?${buildQuery(params)}`, { signal });
const write = <T>(path: string, method: string, body?: unknown) =>
  apiRequest<T>(`${base}${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) });

export const sshApi = {
  connections: (signal?: AbortSignal) => get<{ items: SshConnection[] }>("/connections", {}, signal),
  saveConnection: (input: SshConnectionInput, id?: number) =>
    write<SshConnection>(id ? `/connections/${id}` : "/connections", id ? "PUT" : "POST", input),
  deleteConnection: (id: number) => write<void>(`/connections/${id}`, "DELETE"),
  testConnection: (id: number) =>
    write<{ ok: boolean; hostKey: string; uname: string }>(`/connections/${id}/test`, "POST"),
  exec: (id: number, input: { command: string; timeoutSeconds?: number; pty?: boolean }) =>
    write<SshExecOutcome>(`/connections/${id}/exec`, "POST", input),
  command: (id: number, signal?: AbortSignal) => get<SshExecOutcome>(`/commands/${id}`, {}, signal),
  pending: (signal?: AbortSignal) => get<{ items: SshExecLog[] }>("/pending-commands", {}, signal),
  approve: (id: number) => write<SshExecLog>(`/commands/${id}/approve`, "POST"),
  reject: (id: number) => write<SshExecLog>(`/commands/${id}/reject`, "POST"),
  logs: (params: SshLogQuery, signal?: AbortSignal) => get<SshLogPage>("/exec-logs", params, signal),
  settings: (signal?: AbortSignal) => get<SshSettings>("/settings", {}, signal),
  saveSettings: (input: SshSettings) => write<SshSettings>("/settings", "PUT", input),
  openSession: (id: number, input: { rows?: number; cols?: number; commandId?: number }) =>
    write<SshSessionInfo>(`/connections/${id}/sessions`, "POST", input),
  /** 为已有会话再签一张一次性票据：重新打开面板 / 断线重连时接回持久会话 */
  sessionTicket: (id: number, sid: string) =>
    write<{ ticket: string }>(`/connections/${id}/sessions/${sid}/ticket`, "POST"),
  resizeSession: (id: number, sid: string, rows: number, cols: number) =>
    write<void>(`/connections/${id}/sessions/${sid}/resize`, "POST", { rows, cols }),
  /** 主动结束会话（关闭面板不会结束会话） */
  closeSession: (id: number, sid: string) => write<void>(`/connections/${id}/sessions/${sid}`, "DELETE"),
  /** 给会话起名（标签页上显示）；空串恢复默认的「会话 #N」 */
  renameSession: (id: number, sid: string, name: string) =>
    write<{ name: string }>(`/connections/${id}/sessions/${sid}/rename`, "POST", { name }),
  /** 列出某连接上已打开的终端会话 */
  sessions: (id: number, signal?: AbortSignal) =>
    get<{ items: SshSessionSummary[] }>(`/connections/${id}/sessions`, {}, signal),
  /** WebSocket 地址：直连本地 API 端口（dev 由 vite proxy 转发） */
  sessionSocketUrl: (id: number, sid: string, ticket: string) => {
    const origin = getApiBase() || window.location.origin;
    const url = new URL(`${base}/connections/${id}/sessions/${sid}/ws`, origin);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("ticket", ticket);
    return url.toString();
  },
};

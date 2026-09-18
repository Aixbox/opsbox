import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Choice } from "~/features/fields";
import { DataTable, DetailDialog, PageHeader, QueryError, RefreshButton, dateTime } from "~/features/shared";
import { KindChip, LogStatusChip, formatDuration, kindLabels, sshKey } from "~/features/ssh/shared";
import { sshApi, type LogKind, type LogStatus, type SshExecLog } from "~/lib/api/ssh";
import { formatBytes } from "~/lib/format";

const statusOptions: { value: LogStatus | ""; label: string }[] = [
  { value: "", label: "全部状态" },
  { value: "pending", label: "待批准" },
  { value: "running", label: "执行中" },
  { value: "success", label: "成功" },
  { value: "failed", label: "失败" },
  { value: "timeout", label: "超时" },
  { value: "blocked", label: "已拦截" },
  { value: "rejected", label: "已拒绝" },
];

export default function SshLogsPage() {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [connectionId, setConnectionId] = useState("");
  const [status, setStatus] = useState<LogStatus | "">("");
  const [kind, setKind] = useState<LogKind | "">("");
  const [detail, setDetail] = useState<SshExecLog | null>(null);
  const connections = useQuery({
    queryKey: [...sshKey, "connections"],
    queryFn: ({ signal }) => sshApi.connections(signal),
  });
  const logs = useQuery({
    queryKey: [...sshKey, "logs", { page, pageSize, connectionId, status, kind }],
    queryFn: ({ signal }) =>
      sshApi.logs(
        { page, pageSize, connectionId: connectionId ? Number(connectionId) : undefined, status, kind },
        signal,
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  });
  return (
    <div className="min-w-0 space-y-6">
      <PageHeader
        title="SSH 执行日志"
        description="每条命令 / 传输 / 终端会话的审计：哪个连接、什么命令、退出码、耗时。命令输出加密保存在本机，过了设置的保存天数只剩元数据。"
      >
        <RefreshButton
          pending={logs.isFetching}
          onPress={() => {
            void logs.refetch();
          }}
        />
      </PageHeader>
      <div className="grid gap-3 sm:grid-cols-3">
        <Choice
          label="连接"
          surface={false}
          value={connectionId}
          onChange={(value) => {
            setConnectionId(value);
            setPage(1);
          }}
          options={[
            { value: "", label: "全部连接" },
            ...(connections.data?.items ?? []).map((item) => ({ value: String(item.id), label: item.name })),
          ]}
        />
        <Choice
          label="状态"
          surface={false}
          value={status}
          onChange={(value) => {
            setStatus(value as LogStatus | "");
            setPage(1);
          }}
          options={statusOptions}
        />
        <Choice
          label="类型"
          surface={false}
          value={kind}
          onChange={(value) => {
            setKind(value as LogKind | "");
            setPage(1);
          }}
          options={[
            { value: "", label: "全部类型" },
            ...Object.entries(kindLabels).map(([value, label]) => ({ value, label })),
          ]}
        />
      </div>
      <QueryError
        error={logs.error}
        retry={() => {
          void logs.refetch();
        }}
      />
      <DataTable
        label="执行日志"
        rows={logs.data?.items ?? []}
        loading={logs.isPending}
        failed={logs.isError}
        empty="暂无记录。"
        pagination={{
          page,
          pageSize,
          total: logs.data?.total ?? 0,
          pending: logs.isFetching,
          onChange: setPage,
          onPageSizeChange: (size) => {
            setPageSize(size);
            setPage(1);
          },
        }}
        columns={[
          {
            key: "time",
            label: "时间",
            render: (row) => (
              <div className="text-sm">
                <p>{dateTime(row.createdAt)}</p>
                <p className="mt-1 text-xs text-muted">#{row.id}</p>
              </div>
            ),
          },
          {
            key: "target",
            label: "连接",
            render: (row) => (
              <div className="text-sm">
                <p className="font-medium">{row.connectionName || `#${row.connectionId}`}</p>
              </div>
            ),
          },
          {
            key: "command",
            label: "命令",
            render: (row) => (
              <button type="button" className="max-w-md text-left" onClick={() => setDetail(row)}>
                <span className="mb-1 inline-block">
                  <KindChip kind={row.kind} />
                </span>
                <span className="block truncate font-mono text-xs" title={row.command}>
                  {row.command}
                </span>
              </button>
            ),
          },
          {
            key: "status",
            label: "状态",
            render: (row) => (
              <div className="space-y-1">
                <LogStatusChip status={row.status} />
                {row.error && <p className="line-clamp-2 max-w-56 text-xs text-danger">{row.error}</p>}
              </div>
            ),
          },
          {
            key: "result",
            label: "退出码 / 耗时",
            render: (row) => (
              <div className="text-sm">
                <p>{row.exitCode ?? "—"}</p>
                <p className="mt-1 text-xs text-muted">{formatDuration(row.durationMs)}</p>
              </div>
            ),
          },
          {
            key: "bytes",
            label: "输出",
            render: (row) => (
              <span className="text-xs text-muted">
                {formatBytes(row.stdoutBytes)} / {formatBytes(row.stderrBytes)}
                {row.truncated ? " · 截断" : ""}
              </span>
            ),
          },
        ]}
      />
      {detail && <LogDetail detail={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

/** 日志详情：元数据 + （exec 类）已落库的输出，输出过保留期时提示 */
function LogDetail({ detail, onClose }: { detail: SshExecLog; onClose: () => void }) {
  const finished = detail.status === "success" || detail.status === "failed" || detail.status === "timeout";
  const output = useQuery({
    queryKey: [...sshKey, "command", detail.id],
    queryFn: ({ signal }) => sshApi.command(detail.id, signal),
    enabled: detail.kind === "exec" && finished,
  });
  return (
    <DetailDialog title={`${kindLabels[detail.kind]} #${detail.id}`} onClose={onClose}>
      <dl className="grid gap-3 text-sm sm:grid-cols-2">
        <Item label="连接" value={detail.connectionName} />
        <Item label="状态" value={<LogStatusChip status={detail.status} />} />
        <Item label="退出码" value={detail.exitCode ?? "—"} />
        <Item label="创建时间" value={dateTime(detail.createdAt)} />
        <Item label="完成时间" value={dateTime(detail.finishedAt ?? undefined)} />
        <Item label="耗时" value={formatDuration(detail.durationMs)} />
        <Item
          label={detail.kind === "exec" ? "输出字节" : "传输字节"}
          value={
            detail.kind === "exec"
              ? `stdout ${formatBytes(detail.stdoutBytes)} · stderr ${formatBytes(detail.stderrBytes)}${detail.truncated ? "（已截断）" : ""}`
              : `${formatBytes(detail.stdoutBytes)}${detail.options.totalBytes ? ` / ${formatBytes(detail.options.totalBytes)}` : ""}`
          }
        />
        {detail.options.timeoutSeconds ? (
          <Item label="超时" value={`${detail.options.timeoutSeconds} 秒${detail.options.pty ? " · PTY" : ""}`} />
        ) : null}
      </dl>
      <div>
        <p className="text-sm text-muted">命令 / 路径</p>
        <pre className="mt-1 max-h-64 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 break-all whitespace-pre-wrap">
          {detail.command}
        </pre>
      </div>
      {detail.options.dynamicReason && <p className="text-sm text-warning">静态分析：{detail.options.dynamicReason}</p>}
      {detail.error && <p className="text-sm text-danger">{detail.error}</p>}
      {output.isPending && output.isFetching && <p className="text-sm text-muted">正在读取输出…</p>}
      {output.data?.outputExpired && (
        <p className="text-sm text-muted">输出已超过保存期（或设置为不保存），只剩元数据。</p>
      )}
      {output.data?.stdout && (
        <div>
          <p className="text-sm text-muted">stdout</p>
          <pre className="mt-1 max-h-72 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 whitespace-pre-wrap">
            {output.data.stdout}
          </pre>
        </div>
      )}
      {output.data?.stderr && (
        <div>
          <p className="text-sm text-muted">stderr</p>
          <pre className="mt-1 max-h-48 overflow-auto rounded-lg bg-danger/10 p-3 font-mono text-xs leading-5 whitespace-pre-wrap text-danger">
            {output.data.stderr}
          </pre>
        </div>
      )}
    </DetailDialog>
  );
}

function Item({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted">{label}</dt>
      <dd className="mt-1 font-medium break-words">{value}</dd>
    </div>
  );
}

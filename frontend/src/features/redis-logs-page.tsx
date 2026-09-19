import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Choice } from "~/features/fields";
import { DataTable, DetailDialog, PageHeader, QueryError, RefreshButton, dateTime } from "~/features/shared";
import { LogStatusChip, formatDuration, redisKey } from "~/features/redis/shared";
import { redisApi, type LogStatus, type RedisExecLog } from "~/lib/api/redis";
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

export default function RedisLogsPage() {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [connectionId, setConnectionId] = useState("");
  const [status, setStatus] = useState<LogStatus | "">("");
  const [detail, setDetail] = useState<RedisExecLog | null>(null);
  const connections = useQuery({
    queryKey: [...redisKey, "connections"],
    queryFn: ({ signal }) => redisApi.connections(signal),
  });
  const logs = useQuery({
    queryKey: [...redisKey, "logs", { page, pageSize, connectionId, status }],
    queryFn: ({ signal }) =>
      redisApi.logs({ page, pageSize, connectionId: connectionId ? Number(connectionId) : undefined, status }, signal),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  });
  return (
    <div className="min-w-0 space-y-6">
      <PageHeader
        title="Redis 命令日志"
        description="每条命令的审计：哪个实例、什么命令、状态、耗时、回复字节数。回复 AES-256-GCM 加密保存在本机（默认 7 天），过了设置的保存天数只剩元数据。"
      >
        <RefreshButton
          pending={logs.isFetching}
          onPress={() => {
            void logs.refetch();
          }}
        />
      </PageHeader>
      <div className="grid gap-3 sm:grid-cols-2">
        <Choice
          label="连接"
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
          value={status}
          onChange={(value) => {
            setStatus(value as LogStatus | "");
            setPage(1);
          }}
          options={statusOptions}
        />
      </div>
      <QueryError
        error={logs.error}
        retry={() => {
          void logs.refetch();
        }}
      />
      <DataTable
        label="命令日志"
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
            label: "耗时 / 回复",
            render: (row) => (
              <div className="text-sm">
                <p>{formatDuration(row.durationMs)}</p>
                <p className="mt-1 text-xs text-muted">
                  {formatBytes(row.replyBytes)}
                  {row.truncated ? " · 截断" : ""}
                </p>
              </div>
            ),
          },
        ]}
      />
      {detail && <LogDetail detail={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

/** 日志详情：元数据 + 保留期内的加密落库回复，回复过保存期时提示只剩元数据 */
function LogDetail({ detail, onClose }: { detail: RedisExecLog; onClose: () => void }) {
  const finished = detail.status === "success" || detail.status === "failed" || detail.status === "timeout";
  const output = useQuery({
    queryKey: [...redisKey, "command", detail.id],
    queryFn: ({ signal }) => redisApi.command(detail.id, signal),
    enabled: finished,
  });
  return (
    <DetailDialog title={`命令 #${detail.id}`} onClose={onClose}>
      <dl className="grid gap-3 text-sm sm:grid-cols-2">
        <Item label="连接" value={detail.connectionName} />
        <Item label="状态" value={<LogStatusChip status={detail.status} />} />
        <Item label="耗时" value={formatDuration(detail.durationMs)} />
        <Item label="回复字节" value={`${formatBytes(detail.replyBytes)}${detail.truncated ? "（已截断）" : ""}`} />
        <Item label="创建时间" value={dateTime(detail.createdAt)} />
        <Item label="完成时间" value={dateTime(detail.finishedAt ?? undefined)} />
      </dl>
      <div>
        <p className="text-sm text-muted">命令</p>
        <pre className="mt-1 max-h-40 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 break-all whitespace-pre-wrap">
          {detail.command}
        </pre>
      </div>
      {detail.options.reason && <p className="text-sm text-warning">分类说明：{detail.options.reason}</p>}
      {detail.error && <p className="text-sm text-danger">{detail.error}</p>}
      {output.isPending && output.isFetching && <p className="text-sm text-muted">正在读取回复…</p>}
      {output.data?.replyNote && <p className="text-sm text-muted">{output.data.replyNote}</p>}
      {output.data?.text && (
        <div>
          <p className="text-sm text-muted">回复</p>
          <pre className="mt-1 max-h-72 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 whitespace-pre-wrap">
            {output.data.text}
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

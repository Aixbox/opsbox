import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Choice } from "~/features/fields";
import { DataTable, DetailDialog, PageHeader, QueryError, RefreshButton, dateTime } from "~/features/shared";
import { KindChip, LogStatusChip, ResultTable, formatDuration, kindLabels, sqlKey } from "~/features/sql/shared";
import { sqlApi, type QueryKind, type QueryStatus, type SqlQueryLog } from "~/lib/api/sql";

const statusOptions: { value: QueryStatus | ""; label: string }[] = [
  { value: "", label: "全部状态" },
  { value: "pending", label: "待批准" },
  { value: "running", label: "执行中" },
  { value: "success", label: "成功" },
  { value: "failed", label: "失败" },
  { value: "timeout", label: "超时" },
  { value: "blocked", label: "已拦截" },
  { value: "rejected", label: "已拒绝" },
];

export default function SqlLogsPage() {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [connectionId, setConnectionId] = useState("");
  const [status, setStatus] = useState<QueryStatus | "">("");
  const [kind, setKind] = useState<QueryKind | "">("");
  const [detail, setDetail] = useState<SqlQueryLog | null>(null);
  const connections = useQuery({
    queryKey: [...sqlKey, "connections"],
    queryFn: ({ signal }) => sqlApi.connections(signal),
  });
  const logs = useQuery({
    queryKey: [...sqlKey, "logs", { page, pageSize, connectionId, status, kind }],
    queryFn: ({ signal }) =>
      sqlApi.logs(
        { page, pageSize, connectionId: connectionId ? Number(connectionId) : undefined, status, kind },
        signal,
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  });
  return (
    <div className="min-w-0 space-y-6">
      <PageHeader
        title="SQL 查询日志"
        description="每条 SQL 的审计：哪个库、什么语句、读写类别、状态、行数、耗时。查询结果加密落库（保留天数见设置），详情里可回看；日志本身保留 90 天。"
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
            setStatus(value as QueryStatus | "");
            setPage(1);
          }}
          options={statusOptions}
        />
        <Choice
          label="类别"
          surface={false}
          value={kind}
          onChange={(value) => {
            setKind(value as QueryKind | "");
            setPage(1);
          }}
          options={[
            { value: "", label: "全部类别" },
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
        label="查询日志"
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
            key: "statements",
            label: "语句",
            render: (row) => (
              <button type="button" className="max-w-md text-left" onClick={() => setDetail(row)}>
                <span className="mb-1 inline-block">
                  <KindChip kind={row.kind} />
                </span>
                <span className="block truncate font-mono text-xs" title={row.statements}>
                  {row.statements}
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
            label: "行数 / 耗时",
            render: (row) => (
              <div className="text-sm">
                <p>{row.kind === "read" ? `${row.rowsReturned} 行` : `影响 ${row.rowsAffected} 行`}</p>
                <p className="mt-1 text-xs text-muted">{formatDuration(row.durationMs)}</p>
              </div>
            ),
          },
          {
            key: "flags",
            label: "标记",
            render: (row) => (
              <span className="text-xs text-muted">
                {row.truncated ? "截断" : ""}
                {row.options.tx ? (row.truncated ? " · 事务" : "事务") : ""}
                {!row.truncated && !row.options.tx ? "—" : ""}
              </span>
            ),
          },
        ]}
      />
      {detail && <LogDetail detail={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

/** 日志详情：元数据 + 已落库的查询结果（保留期内可回看，过期只剩元数据） */
function LogDetail({ detail, onClose }: { detail: SqlQueryLog; onClose: () => void }) {
  const finished = detail.status === "success" || detail.status === "failed" || detail.status === "timeout";
  const output = useQuery({
    queryKey: [...sqlKey, "query", detail.id],
    queryFn: ({ signal }) => sqlApi.queryStatus(detail.id, signal),
    enabled: finished,
  });
  return (
    <DetailDialog title={`SQL #${detail.id}`} onClose={onClose}>
      <dl className="grid gap-3 text-sm sm:grid-cols-2">
        <Item label="连接" value={detail.connectionName} />
        <Item label="状态" value={<LogStatusChip status={detail.status} />} />
        <Item label="类别" value={kindLabels[detail.kind] ?? detail.kind} />
        <Item label="创建时间" value={dateTime(detail.createdAt)} />
        <Item label="完成时间" value={dateTime(detail.finishedAt ?? undefined)} />
        <Item label="耗时" value={formatDuration(detail.durationMs)} />
        <Item
          label={detail.kind === "read" ? "返回行数" : "影响行数"}
          value={
            detail.kind === "read"
              ? `${detail.rowsReturned}${detail.truncated ? "（已截断）" : ""}`
              : String(detail.rowsAffected)
          }
        />
        {detail.options.timeoutSeconds ? (
          <Item label="超时" value={`${detail.options.timeoutSeconds} 秒${detail.options.tx ? " · 事务" : ""}`} />
        ) : null}
      </dl>
      <div>
        <p className="text-sm text-muted">SQL</p>
        <pre className="mt-1 max-h-64 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 break-all whitespace-pre-wrap">
          {detail.statements}
        </pre>
      </div>
      {detail.error && <p className="text-sm text-danger">{detail.error}</p>}
      {detail.kind === "read" && finished && (
        <div className="space-y-1">
          <p className="text-sm text-muted">查询结果</p>
          {output.isFetching && <p className="text-sm text-muted">正在读取结果…</p>}
          {output.data?.outputExpired && (
            <p className="text-sm text-muted">结果已超过保存期（或设置为不保存），只剩元数据。</p>
          )}
          {output.data?.columns && output.data.columns.length > 0 && (
            <ResultTable columns={output.data.columns} rows={output.data.rows} />
          )}
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

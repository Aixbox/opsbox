import { Chip } from "@heroui/react";
import type { QueryKind, QueryStatus, WritePolicy } from "~/lib/api/sql";

export const sqlKey = ["sql"] as const;

export const engineLabels: Record<string, string> = {
  mysql: "MySQL",
  postgres: "PostgreSQL",
};

export const policyLabels: Record<WritePolicy, string> = {
  readonly: "只读",
  confirm: "写需审批",
  allow: "全放行",
};

export const kindLabels: Record<QueryKind, string> = {
  read: "读",
  write: "写",
  schema: "自省",
};

const statusMeta: Record<
  QueryStatus,
  { label: string; color: "success" | "warning" | "danger" | "default" | "accent" }
> = {
  pending: { label: "待批准", color: "warning" },
  running: { label: "执行中", color: "accent" },
  success: { label: "成功", color: "success" },
  failed: { label: "失败", color: "danger" },
  timeout: { label: "超时", color: "danger" },
  blocked: { label: "已拦截", color: "danger" },
  rejected: { label: "已拒绝", color: "default" },
};

export function LogStatusChip({ status }: { status: QueryStatus }) {
  const meta = statusMeta[status] ?? { label: status, color: "default" as const };
  return (
    <Chip size="sm" variant="soft" color={meta.color}>
      <Chip.Label>{meta.label}</Chip.Label>
    </Chip>
  );
}

export function PolicyChip({ policy }: { policy: WritePolicy }) {
  return (
    <Chip size="sm" variant="soft" color={policy === "allow" ? "danger" : policy === "confirm" ? "warning" : "success"}>
      <Chip.Label>{policyLabels[policy]}</Chip.Label>
    </Chip>
  );
}

export function KindChip({ kind }: { kind: QueryKind }) {
  return (
    <Chip size="sm" variant="soft" color={kind === "write" ? "warning" : "default"}>
      <Chip.Label>{kindLabels[kind] ?? kind}</Chip.Label>
    </Chip>
  );
}

export function formatDuration(ms: number) {
  if (!ms) return "—";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60_000)} 分 ${Math.round((ms % 60_000) / 1000)} 秒`;
}

/** 黑名单规则的展示名与说明（与后端 sqlx.AllRules 对齐） */
export const blockedRuleMeta: Record<string, { label: string; description: string }> = {
  drop_database: { label: "DROP DATABASE", description: "删除整个数据库（含全部表）" },
  drop_schema: { label: "DROP SCHEMA", description: "删除整个模式（含全部对象）" },
  full_table_write: { label: "无 WHERE 的 UPDATE / DELETE", description: "整表改写，经典误删源" },
  truncate: { label: "TRUNCATE", description: "清空整张表" },
  drop_table: { label: "DROP TABLE", description: "删除整张表" },
};

/** 查询结果的表格视图（查询弹窗与日志详情共用）。
 * 显式 text-foreground：在 SQL 控制台里它会继承深底控制台的浅色文字，
 * 而外层裹的是白色卡片（bg-surface）——不设就是白底近白字。 */
export function ResultTable({ columns, rows }: { columns: string[]; rows?: unknown[][] }) {
  return (
    <div className="max-h-72 overflow-auto rounded-lg border border-separator text-foreground">
      <table className="w-full text-left text-xs">
        <thead className="sticky top-0 bg-surface-secondary">
          <tr>
            {columns.map((column) => (
              <th key={column} className="px-2 py-1.5 font-medium whitespace-nowrap">
                {column}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {(rows ?? []).map((row, index) => (
            <tr key={index} className="border-t border-separator">
              {row.map((value, cell) => (
                <td key={cell} className="max-w-64 truncate px-2 py-1.5 font-mono" title={String(value ?? "NULL")}>
                  {value === null || value === undefined ? <span className="text-muted">NULL</span> : String(value)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

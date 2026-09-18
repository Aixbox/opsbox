import { ApprovalPanel, type ApprovalItem } from "~/features/approval-panel";
import { sqlApi, type SqlQueryLog } from "~/lib/api/sql";
import { KindChip } from "./shared";

/** 把 SQL 日志行归一成审批面板的视图模型 */
export function toApprovalItem(item: SqlQueryLog): ApprovalItem {
  const notes = [
    item.options.timeoutSeconds ? `超时 ${item.options.timeoutSeconds}s` : "",
    item.options.tx ? "事务包裹" : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return {
    id: item.id,
    connectionName: item.connectionName,
    username: item.username,
    createdAt: item.createdAt,
    command: item.statements,
    note: notes,
    meta: <KindChip kind={item.kind} />,
  };
}

/** 待批准写操作面板：逐条批准 / 拒绝（AlertDialog 二次确认），与 SSH 审批面板同构 */
export function PendingPanel({
  items,
  onChanged,
  compact = false,
}: {
  items: SqlQueryLog[];
  onChanged: () => Promise<unknown>;
  compact?: boolean;
}) {
  return (
    <ApprovalPanel
      items={items.map(toApprovalItem)}
      noun="写操作"
      compact={compact}
      onApprove={(id) => sqlApi.approve(id)}
      onReject={(id) => sqlApi.reject(id)}
      onChanged={onChanged}
    />
  );
}

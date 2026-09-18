import { ApprovalPanel, type ApprovalItem } from "~/features/approval-panel";
import { sshApi, type SshExecLog } from "~/lib/api/ssh";
import { KindChip, kindLabels } from "./shared";

/** 把 SSH 日志行归一成审批面板的视图模型 */
export function toApprovalItem(item: SshExecLog): ApprovalItem {
  const notes = [
    item.options.timeoutSeconds ? `超时 ${item.options.timeoutSeconds}s` : "",
    item.options.pty ? "PTY" : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return {
    id: item.id,
    connectionName: item.connectionName,
    username: item.username,
    createdAt: item.createdAt,
    command: item.command,
    note: [notes, item.options.dynamicReason ? `静态分析：${item.options.dynamicReason}` : ""]
      .filter(Boolean)
      .join(" · "),
    meta: <KindChip kind={item.kind} />,
  };
}

/** 待批准操作面板：逐条批准 / 拒绝（AlertDialog 二次确认） */
export function PendingPanel({
  items,
  onChanged,
  compact = false,
}: {
  items: SshExecLog[];
  onChanged: () => Promise<unknown>;
  compact?: boolean;
}) {
  return (
    <ApprovalPanel
      items={items.map(toApprovalItem)}
      noun="操作"
      compact={compact}
      onApprove={(id) => sshApi.approve(id)}
      onReject={(id) => sshApi.reject(id)}
      onChanged={onChanged}
      approveLabel={items.some((item) => item.kind === "exec") ? "批准并执行" : "批准"}
    />
  );
}

/** 供确认文案复用的类型标签 */
export { kindLabels };

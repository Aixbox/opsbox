import { ApprovalPanel, type ApprovalItem } from "~/features/approval-panel";
import { redisApi, type RedisExecLog } from "~/lib/api/redis";
import { LogStatusChip } from "./shared";

/** 把 Redis 日志行归一成审批面板的视图模型 */
export function toApprovalItem(item: RedisExecLog): ApprovalItem {
  return {
    id: item.id,
    connectionName: item.connectionName,
    username: item.username,
    createdAt: item.createdAt,
    command: item.command,
    note: item.options.reason ? `分类说明：${item.options.reason}` : "",
    meta: <LogStatusChip status={item.status} />,
  };
}

/** 待批准命令面板：逐条批准 / 拒绝（AlertDialog 二次确认），批准后后台异步执行 */
export function PendingPanel({
  items,
  onChanged,
  compact = false,
}: {
  items: RedisExecLog[];
  onChanged: () => Promise<unknown>;
  compact?: boolean;
}) {
  return (
    <ApprovalPanel
      items={items.map(toApprovalItem)}
      noun="命令"
      compact={compact}
      onApprove={(id) => redisApi.approve(id)}
      onReject={(id) => redisApi.reject(id)}
      onChanged={onChanged}
    />
  );
}

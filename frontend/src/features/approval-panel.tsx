import { Button } from "@heroui/react";
import { useState, type ReactNode } from "react";
import { ConfirmDialog, dateTime, type Confirmation } from "~/features/shared";

/** 待批准项的统一视图模型：三个模块的日志行都归一到这几个字段 */
export interface ApprovalItem {
  id: number;
  connectionName: string;
  username: string;
  createdAt: string;
  /** 展示给审批人看的命令 / 语句原文 */
  command: string;
  /** 补充说明：分类原因、静态分析结论等 */
  note?: string;
  /** 行内标签（状态 chip、类型 chip、超时提示…），由各模块自行渲染 */
  meta?: ReactNode;
}

export interface ApprovalPanelProps {
  items: ApprovalItem[];
  /** 批准并执行；抛错时由 ConfirmDialog 展示 */
  onApprove: (id: number) => Promise<unknown>;
  onReject: (id: number) => Promise<unknown>;
  /** 审批完成后的刷新（刷新待批列表与审计数据） */
  onChanged: () => Promise<unknown>;
  /** 动作名称，用于确认文案：「批准并执行」「批准命令」等 */
  approveLabel?: string;
  /** 审批对象的称呼：「操作」「命令」「写操作」 */
  noun?: string;
  /** 侧栏排布：更紧凑、去掉外层卡片边框（用于终端 / 控制台抽屉右侧） */
  compact?: boolean;
}

/**
 * 待批准面板（SSH / Redis / SQL 共用）。
 *
 * 两处用法：连接列表页顶部是整块卡片（跨连接的全局视图），
 * 终端 / 控制台抽屉右侧是 compact 侧栏——审批入口贴着会话，不用退出面板去别处批准。
 */
export function ApprovalPanel({
  items,
  onApprove,
  onReject,
  onChanged,
  approveLabel = "批准并执行",
  noun = "操作",
  compact = false,
}: ApprovalPanelProps) {
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  if (items.length === 0) return null;
  return (
    <section className={compact ? "space-y-2" : "space-y-3 rounded-2xl border border-warning/40 bg-warning/5 p-4"}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className={compact ? "text-sm font-semibold" : "text-base font-semibold"}>
          待批准{noun}（{items.length}）
        </h2>
        {!compact && <p className="text-xs text-muted">批准后在服务端异步执行，发起方（CLI / AI）会自动轮询到结果。</p>}
      </div>
      <ul className="space-y-3">
        {items.map((item) => (
          <li key={item.id} className="rounded-xl bg-surface p-3 shadow-sm">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="font-medium">{item.connectionName}</span>
              <span className="text-xs text-muted">
                #{item.id} · {item.username || "未知用户"}
              </span>
              {item.meta}
            </div>
            {!compact && <p className="mt-1 text-xs text-muted">{dateTime(item.createdAt)}</p>}
            <pre className="mt-2 max-h-40 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 break-all whitespace-pre-wrap">
              {item.command}
            </pre>
            {item.note && <p className="mt-2 text-xs text-warning">{item.note}</p>}
            <div className="mt-3 flex flex-wrap justify-end gap-2">
              <Button
                size="sm"
                variant="tertiary"
                onPress={() =>
                  setConfirmation({
                    title: `拒绝${noun} #${item.id}`,
                    description: "发起方会收到「已被拒绝」并停止等待。",
                    danger: true,
                    label: "拒绝",
                    action: async () => {
                      await onReject(item.id);
                      await onChanged();
                    },
                  })
                }
              >
                拒绝
              </Button>
              <Button
                size="sm"
                onPress={() =>
                  setConfirmation({
                    title: `批准${noun} #${item.id}`,
                    description: `将在 ${item.connectionName} 上执行：${item.command.slice(0, 200)}`,
                    label: approveLabel,
                    action: async () => {
                      await onApprove(item.id);
                      await onChanged();
                    },
                  })
                }
              >
                批准
              </Button>
            </div>
          </li>
        ))}
      </ul>
      <ConfirmDialog confirmation={confirmation} onClose={() => setConfirmation(null)} />
    </section>
  );
}

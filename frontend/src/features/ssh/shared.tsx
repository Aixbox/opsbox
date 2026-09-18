import { Chip } from "@heroui/react";
import type { ExecPolicy, LogKind, LogStatus } from "~/lib/api/ssh";

export const sshKey = ["ssh"] as const;

export const policyLabels: Record<ExecPolicy, string> = {
  audit: "审计放行",
  approve: "逐条审批",
};

export const kindLabels: Record<LogKind, string> = {
  exec: "命令",
  upload: "上传",
  download: "下载",
  session: "终端",
};

const statusMeta: Record<LogStatus, { label: string; color: "success" | "warning" | "danger" | "default" | "accent" }> =
  {
    pending: { label: "待批准", color: "warning" },
    approved: { label: "已批准", color: "accent" },
    running: { label: "执行中", color: "accent" },
    success: { label: "成功", color: "success" },
    failed: { label: "失败", color: "danger" },
    timeout: { label: "超时", color: "danger" },
    blocked: { label: "已拦截", color: "danger" },
    rejected: { label: "已拒绝", color: "default" },
  };

export function LogStatusChip({ status }: { status: LogStatus }) {
  const meta = statusMeta[status] ?? { label: status, color: "default" as const };
  return (
    <Chip size="sm" variant="soft" color={meta.color}>
      <Chip.Label>{meta.label}</Chip.Label>
    </Chip>
  );
}

export function PolicyChip({ policy }: { policy: ExecPolicy }) {
  return (
    <Chip size="sm" variant="soft" color={policy === "approve" ? "warning" : "success"}>
      <Chip.Label>{policyLabels[policy]}</Chip.Label>
    </Chip>
  );
}

export function KindChip({ kind }: { kind: LogKind }) {
  return (
    <Chip size="sm" variant="soft" color="default">
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

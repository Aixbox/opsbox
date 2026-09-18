import { Chip } from "@heroui/react";
import type { LogStatus, WritePolicy } from "~/lib/api/redis";

export const redisKey = ["redis"] as const;

export const policyLabels: Record<WritePolicy, string> = {
  readonly: "只读",
  confirm: "写命令审批",
  allow: "全放行",
};

const statusMeta: Record<LogStatus, { label: string; color: "success" | "warning" | "danger" | "default" | "accent" }> =
  {
    pending: { label: "待批准", color: "warning" },
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

export function PolicyChip({ policy }: { policy: WritePolicy }) {
  return (
    <Chip size="sm" variant="soft" color={policy === "readonly" ? "accent" : policy === "allow" ? "danger" : "warning"}>
      <Chip.Label>{policyLabels[policy]}</Chip.Label>
    </Chip>
  );
}

export function formatDuration(ms: number) {
  if (!ms) return "—";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60_000)} 分 ${Math.round((ms % 60_000) / 1000)} 秒`;
}

/**
 * 把控制台里的一行命令拆成 Redis 参数数组。
 * 后端收的是 args[]（args[0] 是命令名），所以要在这里读懂引号：
 * `SET k "a b"` → ["SET", "k", "a b"]。支持单引号、双引号和 \" \\ 转义。
 */
export function tokenizeCommand(line: string): string[] {
  const tokens: string[] = [];
  let current = "";
  let quote: '"' | "'" | null = null;
  let started = false;

  for (let index = 0; index < line.length; index += 1) {
    const char = line[index];
    if (quote === "'") {
      // 单引号内一切按字面量处理，只有另一个单引号能结束
      if (char === "'") quote = null;
      else current += char;
      continue;
    }
    if (quote === '"') {
      if (char === "\\" && index + 1 < line.length) {
        index += 1;
        current += line[index];
      } else if (char === '"') {
        quote = null;
      } else {
        current += char;
      }
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
      started = true;
      continue;
    }
    if (char === "\\" && index + 1 < line.length) {
      index += 1;
      current += line[index];
      started = true;
      continue;
    }
    if (char === " " || char === "\t") {
      if (started) {
        tokens.push(current);
        current = "";
        started = false;
      }
      continue;
    }
    current += char;
    started = true;
  }
  if (started) tokens.push(current);
  return tokens;
}

/** 把参数还原成一行可读的命令（用于历史回填与日志展示）。 */
export function formatCommand(args: string[]): string {
  return args
    .map((arg) => (/[\s"']/.test(arg) ? `"${arg.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"` : arg))
    .join(" ");
}

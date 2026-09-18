import { useState } from "react";
import { NumericField, TextFieldArea } from "~/features/fields";
import { FormDialog } from "~/features/shared";
import { redisApi, type RedisSettings } from "~/lib/api/redis";

/** 全局设置：黑名单命令（每行一条，可含子命令如 CONFIG SET）、SCAN 上限、单值截断阈值 */
export function SettingsDialog({
  settings,
  onClose,
  onSaved,
}: {
  settings: RedisSettings;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const [commands, setCommands] = useState(settings.blockedCommands.join("\n"));
  const [scanLimit, setScanLimit] = useState(String(settings.scanKeyLimit));
  const [truncate, setTruncate] = useState(String(settings.valueTruncateBytes));
  const [retentionDays, setRetentionDays] = useState(String(settings.outputRetentionDays ?? 7));
  return (
    <FormDialog
      title="Redis 执行设置"
      description="内置规则始终生效：阻塞 / 订阅 / 事务类命令（SUBSCRIBE、BLPOP、MULTI、SELECT 等）被无状态执行模型直接拒绝；Lua（EVAL）与 FUNCTION 按写命令处理。这里补充命令黑名单。"
      onClose={onClose}
      onSubmit={async () => {
        await redisApi.saveSettings({
          blockedCommands: commands
            .split("\n")
            .map((line) => line.trim())
            .filter(Boolean),
          scanKeyLimit: Number(scanLimit) || 1000,
          valueTruncateBytes: Number(truncate) || 4096,
          outputRetentionDays: Math.max(0, Number(retentionDays) || 0),
        });
        await onSaved();
      }}
    >
      <TextFieldArea
        label="黑名单命令（每行一条，任何写策略下都拒绝）"
        value={commands}
        onChange={setCommands}
        rows={8}
        code
        description={`示例：FLUSHALL 清空所有库；CONFIG SET 动态改配置（前缀匹配，CONFIG GET 不受影响）。默认已含 FLUSHALL / FLUSHDB / SHUTDOWN / DEBUG / MODULE / REPLICAOF / SLAVEOF / SWAPDB / CONFIG SET。`}
      />
      <div className="grid gap-4 sm:grid-cols-3">
        <NumericField
          label="SCAN 单次 key 上限"
          value={scanLimit}
          onChange={setScanLimit}
          min={1}
          max={100000}
          description="scan 助手游标迭代的最大返回数，默认 1000"
        />
        <NumericField
          label="单值截断阈值（字节）"
          value={truncate}
          onChange={setTruncate}
          min={16}
          max={1048576}
          description="单个 string 超过该长度截断并标记；回复总量上限固定 256KB"
        />
        <NumericField
          label="回复保存天数"
          value={retentionDays}
          onChange={setRetentionDays}
          min={0}
          max={90}
          description="成功命令的回复 AES-256-GCM 加密落库，供 CLI 断线后重取与审计回溯；0 表示只留元数据"
        />
      </div>
    </FormDialog>
  );
}

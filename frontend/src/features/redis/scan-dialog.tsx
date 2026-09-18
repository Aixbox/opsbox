import { useState } from "react";
import { Field, NumericField } from "~/features/fields";
import { FormDialog, Notice } from "~/features/shared";
import { redisApi, type RedisConnection, type RedisScanResult } from "~/lib/api/redis";

/** 安全 SCAN 助手（Web 端只读探查 key 分布；游标迭代，不会阻塞实例） */
export function ScanDialog({ connection, onClose }: { connection: RedisConnection; onClose: () => void }) {
  const [pattern, setPattern] = useState("*");
  const [limit, setLimit] = useState("");
  const [result, setResult] = useState<RedisScanResult>();
  return (
    <FormDialog
      title={`扫描 key · ${connection.name}`}
      description="用游标 SCAN 替代 KEYS（KEYS 在大库会阻塞实例）。结果有数量上限，超过即提前停止。"
      onClose={onClose}
      submitLabel="扫描"
      onSubmit={async () => {
        const data = await redisApi.scan(connection.id, {
          pattern,
          limit: Number(limit) || undefined,
        });
        setResult(data);
        // 结果留在弹窗内查看，不自动关闭
        throw new KeepOpen();
      }}
    >
      <Field label="匹配模式（glob）" value={pattern} onChange={setPattern} required placeholder="user:*" />
      <NumericField
        label="key 数量上限（可选，默认取平台设置）"
        value={limit}
        onChange={setLimit}
        min={1}
        max={100000}
      />
      {result && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted">
              {result.total} 个 key · {result.durationMs} ms
              {result.truncated ? " · 达到上限未遍历完" : ""}
            </span>
          </div>
          {result.keys.length === 0 ? (
            <Notice status="warning" title="没有匹配的 key" />
          ) : (
            <pre className="max-h-72 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 whitespace-pre-wrap">
              {result.keys.join("\n")}
            </pre>
          )}
        </div>
      )}
    </FormDialog>
  );
}

/** 用异常打断 FormDialog 的自动关闭；message 为空串时提示不显示 */
class KeepOpen extends Error {
  constructor() {
    super("");
  }
}

import { useState } from "react";
import { NumericField, Toggle } from "~/features/fields";
import { FormDialog } from "~/features/shared";
import { sqlApi, type SqlSettings } from "~/lib/api/sql";
import { blockedRuleMeta } from "./shared";

/** 全局设置：行数上限 / 查询超时 / 结构化黑名单规则 */
export function SettingsDialog({
  settings,
  onClose,
  onSaved,
}: {
  settings: SqlSettings;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const [maxRows, setMaxRows] = useState(String(settings.maxRows));
  const [timeout, setTimeout_] = useState(String(settings.queryTimeoutSeconds));
  const [retentionDays, setRetentionDays] = useState(String(settings.outputRetentionDays));
  const [rules, setRules] = useState<string[]>(settings.blockedPatterns);

  function toggleRule(rule: string) {
    setRules((current) => (current.includes(rule) ? current.filter((item) => item !== rule) : [...current, rule]));
  }

  return (
    <FormDialog
      title="SQL 设置"
      description="行数上限与超时约束每一次 SQL 执行；黑名单在任何写策略（含全放行）下都生效，命中即拦截。"
      onClose={onClose}
      onSubmit={async () => {
        await sqlApi.saveSettings({
          maxRows: Number(maxRows) || 500,
          queryTimeoutSeconds: Number(timeout) || 30,
          outputRetentionDays: Math.max(0, Number(retentionDays) || 0),
          blockedPatterns: rules,
        });
        await onSaved();
      }}
    >
      <div className="grid gap-4 sm:grid-cols-3">
        <NumericField
          label="单次返回行数上限"
          value={maxRows}
          onChange={setMaxRows}
          min={1}
          max={10000}
          description="超出部分截断并标记 truncated"
        />
        <NumericField label="默认查询超时（秒）" value={timeout} onChange={setTimeout_} min={1} max={300} />
        <NumericField
          label="结果保存天数"
          value={retentionDays}
          onChange={setRetentionDays}
          min={0}
          max={90}
          description="查询结果加密落库，供 CLI 断线后重取与回看；0 表示只留元数据"
        />
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium">黑名单规则</p>
        {Object.entries(blockedRuleMeta).map(([rule, meta]) => (
          <Toggle
            key={rule}
            label={meta.label}
            selected={rules.includes(rule)}
            onChange={() => toggleRule(rule)}
            description={meta.description}
          />
        ))}
      </div>
    </FormDialog>
  );
}

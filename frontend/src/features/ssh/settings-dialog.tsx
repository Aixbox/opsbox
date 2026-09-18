import { useState } from "react";
import { NumericField, TextFieldArea, Toggle } from "~/features/fields";
import { FormDialog } from "~/features/shared";
import { sshApi, type SshSettings } from "~/lib/api/ssh";

/** 全局设置：自定义黑名单（正则，每行一条）、默认超时、输出上限、输出保存天数、动态命令强制审批 */
export function SettingsDialog({
  settings,
  onClose,
  onSaved,
}: {
  settings: SshSettings;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const [patterns, setPatterns] = useState(settings.blacklistPatterns.join("\n"));
  const [timeout, setTimeout_] = useState(String(settings.execTimeoutSeconds));
  const [limitKb, setLimitKb] = useState(String(Math.round(settings.outputLimitBytes / 1024)));
  const [retentionDays, setRetentionDays] = useState(String(settings.outputRetentionDays ?? 7));
  const [dynamicApproval, setDynamicApproval] = useState(settings.dynamicRequiresApproval);
  return (
    <FormDialog
      title="SSH 执行设置"
      description="内置高危规则（rm -rf / 系统目录、格式化、写块设备、关机重启、fork 炸弹等）基于 shell 语法树识别，始终生效且不可关闭；这里补充自定义规则。"
      onClose={onClose}
      onSubmit={async () => {
        await sshApi.saveSettings({
          blacklistPatterns: patterns
            .split("\n")
            .map((line) => line.trim())
            .filter(Boolean),
          execTimeoutSeconds: Number(timeout) || 60,
          outputLimitBytes: (Number(limitKb) || 256) * 1024,
          dynamicRequiresApproval: dynamicApproval,
          outputRetentionDays: Math.max(0, Number(retentionDays) || 0),
        });
        await onSaved();
      }}
    >
      <TextFieldArea
        label="自定义黑名单（正则，每行一条，不区分大小写）"
        value={patterns}
        onChange={setPatterns}
        rows={6}
        code
        description={String.raw`示例：\bcrontab\s+-r\b 拦截清空 crontab；iptables\s+-F 拦截清空防火墙。命中即拒绝，任何策略下都生效。`}
      />
      <div className="grid gap-4 sm:grid-cols-3">
        <NumericField
          label="默认命令超时（秒）"
          value={timeout}
          onChange={setTimeout_}
          min={1}
          max={600}
          description="CLI 未指定 --timeout 时使用；上限 600"
        />
        <NumericField
          label="输出保留上限（KB）"
          value={limitKb}
          onChange={setLimitKb}
          min={1}
          max={8192}
          description="stdout / stderr 各自上限，超出保留头尾"
        />
        <NumericField
          label="输出保存天数"
          value={retentionDays}
          onChange={setRetentionDays}
          min={0}
          max={90}
          description="命令输出加密落库，供 CLI 中断后重取与审计回溯；0 表示只留元数据"
        />
      </div>
      <Toggle
        label="动态命令强制审批"
        selected={dynamicApproval}
        onChange={setDynamicApproval}
        description="eval、sh -c、curl | sh、目标含变量的递归删除等无法静态判定的命令，在审计放行策略下也进入待批队列。"
      />
    </FormDialog>
  );
}

import { useState } from "react";
import { Field, TextFieldArea, Toggle } from "~/features/fields";
import { FormDialog, Notice } from "~/features/shared";
import { sshApi, type SshConnection, type SshExecOutcome } from "~/lib/api/ssh";
import { LogStatusChip, formatDuration } from "./shared";

/** Web 端快速执行一条命令（调试 / 验证黑名单用；AI 走 CLI） */
export function ExecDialog({ connection, onClose }: { connection: SshConnection; onClose: () => void }) {
  const [command, setCommand] = useState("");
  const [timeout, setTimeout_] = useState("");
  const [pty, setPty] = useState(false);
  const [outcome, setOutcome] = useState<SshExecOutcome>();
  return (
    <FormDialog
      title={`执行命令 · ${connection.name}`}
      description="非交互执行，返回 stdout / stderr 与退出码。逐条审批策略下会进入待批队列，请到面板批准后查看日志。"
      onClose={onClose}
      submitLabel="执行"
      submitDisabled={!command.trim()}
      onSubmit={async () => {
        const result = await sshApi.exec(connection.id, {
          command,
          timeoutSeconds: Number(timeout) || undefined,
          pty,
        });
        setOutcome(result);
        // 结果留在弹窗内查看，不自动关闭
        throw new KeepOpen();
      }}
    >
      <TextFieldArea label="命令" value={command} onChange={setCommand} rows={3} code required />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="超时（秒，可选）" value={timeout} onChange={setTimeout_} type="number" min={1} max={600} />
        <Toggle label="申请 PTY" selected={pty} onChange={setPty} description="sudo / 需要 TTY 的脚本" />
      </div>
      {outcome && <OutcomeView outcome={outcome} />}
    </FormDialog>
  );
}

/** 用异常打断 FormDialog 的自动关闭；message 为空串时提示不显示 */
class KeepOpen extends Error {
  constructor() {
    super("");
  }
}

function OutcomeView({ outcome }: { outcome: SshExecOutcome }) {
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <LogStatusChip status={outcome.status} />
        <span className="text-muted">#{outcome.id}</span>
        {outcome.exitCode !== undefined && <span className="text-muted">exit {outcome.exitCode}</span>}
        <span className="text-muted">{formatDuration(outcome.durationMs)}</span>
        {outcome.truncated && <span className="text-xs text-warning">输出已截断</span>}
      </div>
      {outcome.status === "pending" && (
        <Notice status="warning" title="已进入待批队列">
          {outcome.dynamicReason ? `静态分析：${outcome.dynamicReason}。` : ""}批准后在执行日志中查看结果。
        </Notice>
      )}
      {outcome.status === "blocked" && (
        <Notice status="danger" title={`已被黑名单拦截 [${outcome.blockedRule}]`}>
          {outcome.blockedReason}
        </Notice>
      )}
      {outcome.error && outcome.status !== "blocked" && (
        <Notice status="danger" title="执行错误">
          {outcome.error}
        </Notice>
      )}
      {outcome.stdout && (
        <pre className="max-h-64 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 whitespace-pre-wrap">
          {outcome.stdout}
        </pre>
      )}
      {outcome.stderr && (
        <pre className="max-h-40 overflow-auto rounded-lg bg-danger/10 p-3 font-mono text-xs leading-5 whitespace-pre-wrap text-danger">
          {outcome.stderr}
        </pre>
      )}
    </div>
  );
}

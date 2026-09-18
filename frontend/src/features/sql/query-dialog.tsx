import { useState } from "react";
import { Field, TextFieldArea, Toggle } from "~/features/fields";
import { FormDialog, Notice } from "~/features/shared";
import { sqlApi, type SqlConnection, type SqlQueryOutcome } from "~/lib/api/sql";
import { LogStatusChip, ResultTable, formatDuration } from "./shared";

/** Web 端快速执行一条 SQL（调试 / 验证用；AI 走 CLI） */
export function QueryDialog({ connection, onClose }: { connection: SqlConnection; onClose: () => void }) {
  const [sql, setSql] = useState("");
  const [timeout, setTimeout_] = useState("");
  const [tx, setTx] = useState(false);
  const [outcome, setOutcome] = useState<SqlQueryOutcome>();
  return (
    <FormDialog
      title={`执行 SQL · ${connection.name}`}
      description="读操作直接执行；写操作按连接策略执行 / 进待批队列。结果不落库，只在本弹窗查看。"
      onClose={onClose}
      submitLabel="执行"
      submitDisabled={!sql.trim()}
      onSubmit={async () => {
        const result = await sqlApi.query(connection.id, {
          sql,
          tx,
          timeoutSeconds: Number(timeout) || undefined,
        });
        setOutcome(result);
        // 结果留在弹窗内查看，不自动关闭
        throw new KeepOpen();
      }}
    >
      <TextFieldArea label="SQL（可多条，分号分隔）" value={sql} onChange={setSql} rows={5} code required />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="超时（秒，可选）" value={timeout} onChange={setTimeout_} type="number" min={1} max={300} />
        <Toggle label="事务包裹（--tx）" selected={tx} onChange={setTx} description="任一语句失败整体回滚" />
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

function OutcomeView({ outcome }: { outcome: SqlQueryOutcome }) {
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <LogStatusChip status={outcome.status} />
        <span className="text-muted">#{outcome.id}</span>
        <span className="text-muted">{formatDuration(outcome.durationMs)}</span>
        {outcome.kind === "read" ? (
          <span className="text-muted">
            {outcome.rowsReturned} 行{outcome.truncated ? "（已截断）" : ""}
          </span>
        ) : (
          <span className="text-muted">影响 {outcome.rowsAffected} 行</span>
        )}
      </div>
      {outcome.status === "pending" && (
        <Notice status="warning" title="已进入待批队列">
          批准后在查询日志中查看结果。
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
      {outcome.columns && outcome.columns.length > 0 && <ResultTable columns={outcome.columns} rows={outcome.rows} />}
    </div>
  );
}

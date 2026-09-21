import { Drawer } from "@heroui/react";
import { useQuery } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { ConsoleShell, NoPendingHint, type ConsoleEntry } from "~/features/console-shell";
import { SessionTabs } from "~/features/session-tabs";
import { useConsoleSession } from "~/features/use-console-session";
import { sessionMention, useSessionList } from "~/features/use-session-list";
import { Toggle } from "~/features/fields";
import { ConfirmDialog, Notice, errorMessage, type Confirmation } from "~/features/shared";
import { sqlApi, type QueryStatus, type SqlConnection, type SqlConsoleEvent } from "~/lib/api/sql";
import { PendingPanel } from "./pending-panel";
import { formatDuration, KindChip, LogStatusChip, ResultTable, sqlKey } from "./shared";

/**
 * SQL 控制台：上方是语句流（CLI / AI 的查询实时出现在这里），下方输入框支持多行。
 * 会话即授权——这个连接有打开着的会话，CLI 才能操作它。
 * 会话是持久的：关掉抽屉只是断开，再打开接回上次的会话并回放历史；可以多开、切换，「结束当前会话」才真正关闭。
 */
export function ConsoleDrawer({ connection, onClose }: { connection: SqlConnection; onClose: () => void }) {
  const executable = true;
  const [entries, setEntries] = useState<ConsoleEntry[]>([]);
  const [tx, setTx] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);

  const pending = useQuery({
    queryKey: [...sqlKey, "pending", connection.id],
    queryFn: ({ signal }) => sqlApi.pending(signal),
    refetchInterval: 4_000,
    enabled: executable,
  });
  const pendingItems = (pending.data?.items ?? []).filter((item) => item.connectionId === connection.id);

  const sessions = useSessionList({
    queryKey: [...sqlKey, "sessions", connection.id],
    list: (signal) => sqlApi.sessions(connection.id, signal),
    create: async () => (await sqlApi.openSession(connection.id)).sessionId,
    close: (sid) => sqlApi.closeSession(connection.id, sid),
    rename: (sid, name) => sqlApi.renameSession(connection.id, sid, name),
    autoCreate: true,
  });

  const onEvent = useCallback((raw: unknown) => {
    const event = raw as SqlConsoleEvent;
    if (event.type !== "query") return;
    setEntries((previous) => [...previous, toEntry(event)]);
  }, []);
  const onReset = useCallback(() => setEntries([]), []);

  const { phase, send } = useConsoleSession({
    sessionId: sessions.activeId,
    issueTicket: (sid) => sqlApi.issueTicket(connection.id, sid),
    socketUrl: (sid, ticket) => sqlApi.consoleSocketUrl(connection.id, sid, ticket),
    onEvent,
    onReset,
    onEnded: sessions.refetch,
  });

  // 面板里手写的 SQL 也由服务端广播回来，所以这里只负责送出去，不本地插入（避免重复）
  function submit(input: string) {
    return send({ sql: input, tx });
  }

  function confirmTerminate(sessionId: string) {
    const item = sessions.items.find((candidate) => candidate.sessionId === sessionId);
    setConfirmation({
      title: `结束${item ? sessionMention(item) : "会话"}`,
      description:
        "这个会话的历史记录会清空，CLI 立即失去它；其他会话不受影响。只是暂时离开的话直接关闭面板即可，会话会保留。",
      danger: true,
      label: "结束会话",
      action: () => sessions.terminate(sessionId),
    });
  }

  return (
    <>
      <Drawer.Backdrop isOpen onOpenChange={(open) => !open && onClose()}>
        <Drawer.Content placement="right">
          <Drawer.Dialog className="w-screen max-w-full lg:max-w-7xl">
            <Drawer.CloseTrigger />
            <Drawer.Header>
              <Drawer.Heading>
                控制台 · {connection.name}
                <span className="ml-2 text-sm font-normal text-muted">
                  {connection.engine} · {connection.username}@{connection.host}:{connection.port}/{connection.database}
                </span>
              </Drawer.Heading>
            </Drawer.Header>
            <Drawer.Body className="flex min-h-0 flex-col gap-3">
              <ConsoleShell
                phase={phase}
                entries={entries}
                placeholder="SELECT id, name FROM users WHERE age > 18;"
                multiline
                onSubmit={submit}
                header={
                  <>
                    <SessionTabs
                      items={sessions.items}
                      activeId={sessions.activeId}
                      onSelect={sessions.select}
                      onCreate={() => void sessions.create()}
                      onTerminate={confirmTerminate}
                      onRename={sessions.rename}
                      creating={sessions.creating}
                      disabled={!sessions.loaded}
                    />
                    {sessions.loadError ? (
                      <Notice status="danger" title="无法读取会话列表">
                        {errorMessage(sessions.loadError)}
                      </Notice>
                    ) : null}
                    {sessions.createError && (
                      <Notice status="danger" title="无法建立会话">
                        {sessions.createError}
                      </Notice>
                    )}
                  </>
                }
                footer={
                  <div className="w-56">
                    <Toggle label="事务包裹" selected={tx} onChange={setTx} description="多条语句任一失败则整体回滚" />
                  </div>
                }
                pending={
                  executable && pendingItems.length > 0 ? (
                    <PendingPanel
                      items={pendingItems}
                      compact
                      onChanged={async () => {
                        await pending.refetch();
                      }}
                    />
                  ) : (
                    <NoPendingHint>
                      没有待批准的写操作。这个连接上的 CLI / AI 写语句会在这里排队，批准后就地执行、结果显示在左侧。
                    </NoPendingHint>
                  )
                }
              />
            </Drawer.Body>
            <Drawer.Footer>
              <p className="text-xs text-muted">
                关闭面板不会结束会话，CLI 仍可操作该连接；要收回许可请用标签页右侧的「结束」。会话闲置一小时自动回收。
              </p>
            </Drawer.Footer>
          </Drawer.Dialog>
        </Drawer.Content>
      </Drawer.Backdrop>
      <ConfirmDialog confirmation={confirmation} onClose={() => setConfirmation(null)} />
    </>
  );
}

function toEntry(event: SqlConsoleEvent): ConsoleEntry {
  const rows = event.rows ?? [];
  return {
    key: `${event.logId ?? "x"}-${event.at}-${event.status ?? ""}`,
    source: event.source,
    command: String(event.command ?? ""),
    meta: (
      <span className="flex items-center gap-2">
        {event.kind && <KindChip kind={event.kind} />}
        {event.status && <LogStatusChip status={event.status as QueryStatus} />}
        {event.durationMs ? <span className="text-[#9ca3af]">{formatDuration(event.durationMs)}</span> : null}
        {event.status !== "pending" && rows.length > 0 ? (
          <span className="text-[#9ca3af]">{rows.length} 行</span>
        ) : null}
      </span>
    ),
    error: event.error,
    note: event.note,
    // 结果表格是浅色组件，放在深色控制台里单独裹一层浅底
    result:
      event.columns && event.columns.length > 0 ? (
        <div className="mt-1 rounded-lg bg-surface p-2">
          <ResultTable columns={event.columns} rows={event.rows} />
        </div>
      ) : null,
  };
}

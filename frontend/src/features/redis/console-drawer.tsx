import { useQuery } from "@tanstack/react-query";
import { Drawer } from "@heroui/react";
import { useCallback, useState } from "react";
import { ConsoleShell, NoPendingHint, type ConsoleEntry } from "~/features/console-shell";
import { SessionTabs } from "~/features/session-tabs";
import { useConsoleSession } from "~/features/use-console-session";
import { sessionMention, useSessionList } from "~/features/use-session-list";
import { ConfirmDialog, Notice, errorMessage, type Confirmation } from "~/features/shared";
import { redisApi, type RedisConnection, type RedisConsoleEvent } from "~/lib/api/redis";
import { PendingPanel } from "./pending-panel";
import { formatDuration, LogStatusChip, redisKey, tokenizeCommand } from "./shared";

/**
 * Redis 控制台：上方是命令流（CLI / AI 的操作实时出现在这里），下方一行输入。
 * 会话即授权——这个连接有打开着的会话，CLI 才能操作它。
 * 会话是持久的：关掉抽屉只是断开，再打开接回上次的会话并回放历史；可以多开、切换，「结束当前会话」才真正关闭。
 */
export function ConsoleDrawer({ connection, onClose }: { connection: RedisConnection; onClose: () => void }) {
  const executable = true;
  const [entries, setEntries] = useState<ConsoleEntry[]>([]);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);

  const pending = useQuery({
    queryKey: [...redisKey, "pending", connection.id],
    queryFn: ({ signal }) => redisApi.pending(signal),
    refetchInterval: 4_000,
    enabled: executable,
  });
  const pendingItems = (pending.data?.items ?? []).filter((item) => item.connectionId === connection.id);

  const sessions = useSessionList({
    queryKey: [...redisKey, "sessions", connection.id],
    list: (signal) => redisApi.sessions(connection.id, signal),
    create: async () => (await redisApi.openSession(connection.id)).sessionId,
    close: (sid) => redisApi.closeSession(connection.id, sid),
    rename: (sid, name) => redisApi.renameSession(connection.id, sid, name),
    autoCreate: true,
  });

  const onEvent = useCallback((raw: unknown) => {
    const event = raw as RedisConsoleEvent;
    if (event.type !== "command") return;
    setEntries((previous) => [...previous, toEntry(event)]);
  }, []);
  const onReset = useCallback(() => setEntries([]), []);

  const { phase, send } = useConsoleSession({
    sessionId: sessions.activeId,
    issueTicket: (sid) => redisApi.issueTicket(connection.id, sid),
    socketUrl: (sid, ticket) => redisApi.consoleSocketUrl(connection.id, sid, ticket),
    onEvent,
    onReset,
    onEnded: sessions.refetch,
  });

  // 面板发起的执行也由服务端广播回来，所以这里只负责送出去，不本地插入（避免重复）
  function submit(input: string) {
    const args = tokenizeCommand(input);
    if (args.length === 0) return false;
    return send({ args });
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
                  {connection.host}:{connection.port} · db{connection.db}
                  {connection.tls ? " · TLS" : ""}
                </span>
              </Drawer.Heading>
            </Drawer.Header>
            <Drawer.Body className="flex min-h-0 flex-col gap-3">
              <ConsoleShell
                phase={phase}
                entries={entries}
                placeholder="HGETALL user:1"
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
                      没有待批准的命令。这个连接上的 CLI / AI 命令会在这里排队，批准后就地执行、结果显示在左侧。
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

function toEntry(event: RedisConsoleEvent): ConsoleEntry {
  const args = typeof event.command === "string" ? event.command : String(event.command ?? "");
  return {
    key: `${event.logId ?? "x"}-${event.at}-${event.status ?? ""}`,
    source: event.source,
    command: args,
    meta: (
      <span className="flex items-center gap-2">
        {event.status && <LogStatusChip status={event.status} />}
        {event.durationMs ? <span className="text-[#9ca3af]">{formatDuration(event.durationMs)}</span> : null}
      </span>
    ),
    error: event.error,
    note: event.note,
    result: event.text ? (
      <pre className="max-h-72 overflow-auto break-all whitespace-pre-wrap text-[#e5e7eb]">{event.text}</pre>
    ) : null,
  };
}

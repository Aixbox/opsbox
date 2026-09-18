import { Button, Drawer, Spinner } from "@heroui/react";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { SessionTabs } from "~/features/session-tabs";
import { sessionMention, useSessionList } from "~/features/use-session-list";
import { ConfirmDialog, Notice, errorMessage, type Confirmation } from "~/features/shared";
import { sshApi, type SshConnection } from "~/lib/api/ssh";
import { PendingPanel } from "./pending-panel";
import { sshKey } from "./shared";
import "@xterm/xterm/css/xterm.css";

type Phase =
  | { kind: "idle" }
  | { kind: "connecting" }
  | { kind: "ready" }
  | { kind: "closed"; reason: string }
  | { kind: "error"; message: string };

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/**
 * Web 终端抽屉：xterm.js + @xterm/addon-attach 直连后端 WebSocket（一次性 ticket 鉴权），
 * 尺寸变化经 REST resize 通知远端。
 *
 * 会话是持久的：关掉抽屉只是断开接入，服务端的 shell 还活着，再打开就接回上次的会话（回放历史输出）；
 * 一个连接可以开多个会话，终端上方的标签页在它们之间切换，标签右侧的「结束」才真正关掉远端 shell。
 *
 * 右侧是待批准面板：CLI / AI 提交的命令在这里就地批准，批准后立刻在左侧这个终端的连接上执行，
 * 命令与输出实时回显——不用退出抽屉去列表页处理。
 */
export function TerminalDrawer({ connection, onClose }: { connection: SshConnection; onClose: () => void }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const disposedRef = useRef(false);
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const [pendingLogId, setPendingLogId] = useState<number | null>(null);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);

  useEffect(() => {
    disposedRef.current = false;
    return () => {
      disposedRef.current = true;
    };
  }, []);

  // 待批准项：只取本连接的（后端返回全局队列，这里按 connectionId 过滤）
  const pending = useQuery({
    queryKey: [...sshKey, "pending", connection.id],
    queryFn: ({ signal }) => sshApi.pending(signal),
    refetchInterval: 4_000,
    enabled: true,
  });
  const pendingItems = (pending.data?.items ?? []).filter((item) => item.connectionId === connection.id);

  // 新建会话：audit 策略直接建；approve 策略先登记待批，在这里轮询直到批准后再真正建立连接
  async function createSession(): Promise<string> {
    const info = await sshApi.openSession(connection.id, { rows: 24, cols: 80 });
    if (info.status !== "pending" && info.sessionId) return info.sessionId;
    setPendingLogId(info.logId);
    try {
      for (;;) {
        await sleep(3000);
        if (disposedRef.current) throw new Error("面板已关闭，停止等待批准");
        const outcome = await sshApi.command(info.logId);
        if (outcome.status === "pending") continue;
        if (outcome.status !== "approved") {
          throw new Error(`会话请求已${outcome.status === "rejected" ? "被拒绝" : "失效"}（${outcome.status}）`);
        }
        const opened = await sshApi.openSession(connection.id, { commandId: info.logId, rows: 24, cols: 80 });
        if (opened.sessionId) return opened.sessionId;
      }
    } finally {
      setPendingLogId(null);
    }
  }

  const sessions = useSessionList({
    queryKey: [...sshKey, "sessions", connection.id],
    list: (signal) => sshApi.sessions(connection.id, signal),
    create: createSession,
    close: (sid) => sshApi.closeSession(connection.id, sid),
    rename: (sid, name) => sshApi.renameSession(connection.id, sid, name),
    autoCreate: true,
  });
  const { activeId, refetch: refetchSessions } = sessions;

  // 接入当前会话：签票据 → xterm + WebSocket。切换会话时旧的连接与终端实例一并销毁，
  // 新终端开在同一个容器里（容器常驻，切换时不会先消失再出现）；
  // 这里不会关闭服务端会话——会话是持久的，关掉抽屉再回来还能接上。
  useEffect(() => {
    if (!activeId) {
      setPhase({ kind: "idle" });
      return;
    }
    let disposed = false;
    let socket: WebSocket | undefined;
    let terminal: import("@xterm/xterm").Terminal | undefined;
    let observer: ResizeObserver | undefined;
    setPhase({ kind: "connecting" });

    async function attach(sid: string) {
      const [{ Terminal }, { FitAddon }, { AttachAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
        import("@xterm/addon-attach"),
      ]);
      const { ticket } = await sshApi.sessionTicket(connection.id, sid);
      if (disposed || !containerRef.current) return;
      terminal = new Terminal({
        cursorBlink: true,
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
        fontSize: 13,
        scrollback: 5000,
        theme: { background: "#0b0f19" },
      });
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(containerRef.current);
      fit.fit();
      socket = new WebSocket(sshApi.sessionSocketUrl(connection.id, sid, ticket));
      socket.binaryType = "arraybuffer";
      const attachAddon = new AttachAddon(socket, { bidirectional: true });
      terminal.loadAddon(attachAddon);
      socket.addEventListener("open", () => {
        if (disposed) return;
        setPhase({ kind: "ready" });
        terminal?.focus();
        void sshApi
          .resizeSession(connection.id, sid, terminal?.rows ?? 24, terminal?.cols ?? 80)
          .catch(() => undefined);
      });
      socket.addEventListener("close", (event) => {
        if (disposed) return;
        setPhase({ kind: "closed", reason: event.reason || "连接已断开" });
        // 会话可能是在别处被结束 / 被回收的：刷新列表让切换条同步
        void refetchSessions();
      });
      socket.addEventListener("error", () => {
        if (!disposed) setPhase({ kind: "error", message: "WebSocket 连接失败" });
      });
      let resizeTimer: ReturnType<typeof setTimeout> | undefined;
      observer = new ResizeObserver(() => {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(() => {
          if (!terminal) return;
          fit.fit();
          void sshApi.resizeSession(connection.id, sid, terminal.rows, terminal.cols).catch(() => undefined);
        }, 120);
      });
      observer.observe(containerRef.current);
    }

    attach(activeId).catch((error) => {
      if (disposed) return;
      setPhase({ kind: "error", message: errorMessage(error) });
      // 票据签发失败多半是会话刚被闲置回收 / 在别处被结束：立即刷新列表，让过期的标签页
      // 消失、空列表触发自动补建，而不是顶着一个已死的会话干等 5 秒轮询
      void refetchSessions();
    });

    return () => {
      disposed = true;
      observer?.disconnect();
      socket?.close();
      terminal?.dispose();
    };
  }, [connection.id, activeId, refetchSessions]);

  function confirmTerminate(sessionId: string) {
    const item = sessions.items.find((candidate) => candidate.sessionId === sessionId);
    setConfirmation({
      title: `结束${item ? sessionMention(item) : "会话"}`,
      description:
        "远端 shell 会被关闭，CLI 立即失去这个会话；其他会话不受影响。只是暂时离开的话直接关闭抽屉即可，会话会保留（无人接入且无操作超过一小时才会被回收）。",
      danger: true,
      label: "结束会话",
      action: () => sessions.terminate(sessionId),
    });
  }

  const busy = sessions.creating || !sessions.loaded;
  // 终端框右上角的进行中提示：读取列表 → 建立会话（等待审批时另有 Notice）→ 接入会话
  const busyHint = !sessions.loaded
    ? sessions.loadError
      ? null
      : "正在读取会话…"
    : sessions.creating && pendingLogId === null
      ? "正在建立会话…"
      : phase.kind === "connecting"
        ? "正在接入会话…"
        : null;
  const showIdle = phase.kind === "idle" && sessions.loaded && !sessions.creating;

  return (
    <>
      <Drawer.Backdrop isOpen onOpenChange={(open) => !open && onClose()}>
        <Drawer.Content placement="right">
          <Drawer.Dialog className="w-screen max-w-full lg:max-w-7xl">
            <Drawer.CloseTrigger />
            <Drawer.Header>
              <Drawer.Heading>
                终端 · {connection.name}
                <span className="ml-2 text-sm font-normal text-muted">
                  {connection.username}@{connection.host}:{connection.port}
                </span>
              </Drawer.Heading>
            </Drawer.Header>
            <Drawer.Body className="flex min-h-0 flex-col gap-3 lg:flex-row">
              <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-3">
                <SessionTabs
                  items={sessions.items}
                  activeId={activeId}
                  onSelect={sessions.select}
                  onCreate={() => void sessions.create()}
                  onTerminate={confirmTerminate}
                  onRename={sessions.rename}
                  creating={sessions.creating}
                  disabled={busy}
                />
                {sessions.loadError ? (
                  <Notice status="danger" title="无法读取会话列表">
                    {errorMessage(sessions.loadError)}
                  </Notice>
                ) : null}
                {pendingLogId !== null && (
                  <Notice status="warning" title={`终端会话 #${pendingLogId} 等待批准`}>
                    该连接为逐条审批策略，请在右侧「待批准」面板批准后自动接入。
                  </Notice>
                )}
                {sessions.createError && (
                  <Notice status="danger" title="无法建立会话">
                    {sessions.createError}
                  </Notice>
                )}
                {/* 终端框常驻：读取 / 建立 / 接入 / 结束 / 出错等状态叠在框内，不在标签栏和终端之间插行——插行会把终端推下去再弹回来，看起来像闪 */}
                <div className="relative min-h-[60vh] flex-1 overflow-hidden rounded-xl bg-[#0b0f19]">
                  <div ref={containerRef} className="absolute inset-0 p-2" aria-label="终端" />
                  {busyHint && (
                    <p className="pointer-events-none absolute top-2 right-3 flex items-center gap-2 text-xs text-[#9ca3af]">
                      <Spinner size="sm" color="current" />
                      {busyHint}
                    </p>
                  )}
                  {(phase.kind === "closed" || phase.kind === "error" || showIdle) && (
                    <div className="absolute inset-x-3 top-3">
                      {phase.kind === "closed" && <Notice title="会话已结束">{phase.reason}</Notice>}
                      {phase.kind === "error" && (
                        <Notice status="danger" title="无法接入终端">
                          {phase.message}
                        </Notice>
                      )}
                      {showIdle && (
                        <Notice title="没有打开的会话">
                          点「新建」开一个终端。会话会一直保留到你主动结束或闲置一小时——关掉抽屉不会结束它。
                        </Notice>
                      )}
                    </div>
                  )}
                </div>
              </div>
              <aside className="flex w-full shrink-0 flex-col gap-3 overflow-y-auto lg:w-80">
                {pendingItems.length > 0 ? (
                  <PendingPanel
                    items={pendingItems}
                    compact
                    onChanged={async () => {
                      await pending.refetch();
                    }}
                  />
                ) : (
                  <p className="rounded-xl border border-separator bg-surface-secondary p-3 text-xs leading-5 text-muted">
                    没有待批准的操作。这个连接上的 CLI / AI
                    命令会在这里排队，批准后立即在左侧终端的连接上执行，命令与输出实时回显。
                  </p>
                )}
                <p className="rounded-xl border border-separator bg-surface-secondary p-3 text-xs leading-5 text-muted">
                  CLI 命令在这个会话的 SSH 连接上另开通道执行：你在终端里跑 vim / top
                  时它照样能执行，输出会回显进来；但它不共享终端的当前目录与环境变量。
                </p>
              </aside>
            </Drawer.Body>
            <Drawer.Footer>
              <p className="mr-auto text-xs text-muted">
                关闭抽屉不会结束会话（闲置一小时才回收）；要断开远端 shell 请用标签页右侧的「结束」。
              </p>
              <Button variant="secondary" onPress={onClose}>
                关闭
              </Button>
            </Drawer.Footer>
          </Drawer.Dialog>
        </Drawer.Content>
      </Drawer.Backdrop>
      <ConfirmDialog confirmation={confirmation} onClose={() => setConfirmation(null)} />
    </>
  );
}

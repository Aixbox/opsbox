import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "~/features/shared";

export type ConsolePhase =
  | { kind: "idle" }
  | { kind: "connecting" }
  | { kind: "ready" }
  | { kind: "closed"; reason: string }
  | { kind: "error"; message: string };

export interface ConsoleSessionOptions {
  /** 当前要接入的会话；null 表示还没有会话（面板显示空状态）。变更时断开旧连接、接入新会话 */
  sessionId: string | null;
  issueTicket: (sessionId: string) => Promise<{ ticket: string }>;
  socketUrl: (sessionId: string, ticket: string) => string;
  /** 收到一条服务端事件（命令执行结果、审批结果…） */
  onEvent: (event: unknown) => void;
  /** 接入（新）会话前调用：面板据此清空旧会话的记录——服务端会回放新会话的历史 */
  onReset?: () => void;
  /** 服务端把会话结束了（用户在别处点了结束 / 闲置回收）：面板据此刷新会话列表 */
  onEnded?: () => void;
}

/**
 * 控制台会话的 WebSocket 骨架：拿一次性票据 → 接入事件流。
 * 会话本身由 useSessionList 管理（持久、可多开、可切换）；这里只负责接入当前选中的那一个。
 * 服务端在接入时会回放历史事件，所以切换 / 重新打开面板不会丢上下文。
 *
 * 返回的 send 用来把面板里手输的命令送给后端执行。
 */
export function useConsoleSession(options: ConsoleSessionOptions) {
  const [phase, setPhase] = useState<ConsolePhase>({ kind: "idle" });
  const socketRef = useRef<WebSocket | null>(null);
  // 回调放进 ref：避免它们每次渲染变化都重连
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const { sessionId } = options;

  useEffect(() => {
    if (!sessionId) {
      setPhase({ kind: "idle" });
      return;
    }
    let disposed = false;
    setPhase({ kind: "connecting" });
    optionsRef.current.onReset?.();
    void (async () => {
      try {
        const current = optionsRef.current;
        const { ticket } = await current.issueTicket(sessionId);
        if (disposed) return;
        const socket = new WebSocket(current.socketUrl(sessionId, ticket));
        socketRef.current = socket;
        socket.addEventListener("open", () => {
          if (!disposed) setPhase({ kind: "ready" });
        });
        socket.addEventListener("message", (event) => {
          if (typeof event.data !== "string") return;
          try {
            optionsRef.current.onEvent(JSON.parse(event.data));
          } catch {
            // 无法解析的帧直接忽略，不影响后续事件
          }
        });
        socket.addEventListener("close", (event) => {
          if (disposed) return;
          setPhase({ kind: "closed", reason: event.reason || "连接已断开" });
          optionsRef.current.onEnded?.();
        });
        socket.addEventListener("error", () => {
          if (!disposed) setPhase({ kind: "error", message: "WebSocket 连接失败" });
        });
      } catch (error) {
        if (!disposed) setPhase({ kind: "error", message: errorMessage(error) });
      }
    })();

    // 只断开接入，不结束服务端会话：会话是持久的，重新打开面板还能接回来
    return () => {
      disposed = true;
      socketRef.current?.close();
      socketRef.current = null;
    };
  }, [sessionId]);

  const send = useCallback((payload: unknown) => {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) return false;
    socket.send(JSON.stringify(payload));
    return true;
  }, []);

  return { phase, send };
}

import { Button, Chip, Spinner } from "@heroui/react";
import { Play } from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Notice } from "~/features/shared";
import type { ConsolePhase } from "./use-console-session";

/** 控制台里的一条记录：输入的命令与其结果 */
export interface ConsoleEntry {
  key: string;
  source: "cli" | "web" | "approved";
  command: string;
  status?: string;
  /** 结果区，由各模块自行渲染（Redis 文本 / SQL 表格） */
  result?: ReactNode;
  error?: string;
  note?: string;
  meta?: ReactNode;
}

const sourceLabels: Record<ConsoleEntry["source"], { label: string; color: "accent" | "default" | "warning" }> = {
  cli: { label: "CLI", color: "accent" },
  web: { label: "本机", color: "default" },
  approved: { label: "审批", color: "warning" },
};

/**
 * REPL 控制台外壳：顶部是会话切换条，上方是滚动的历史（CLI 的操作也会出现在这里），下方是输入行。
 * Redis 单行 Enter 执行；SQL 多行 Ctrl+Enter 执行、Enter 换行；都有「执行」按钮；↑↓ 翻历史。
 */
export function ConsoleShell({
  phase,
  entries,
  placeholder,
  multiline = false,
  pending,
  onSubmit,
  header,
  footer,
  disabledHint,
}: {
  phase: ConsolePhase;
  entries: ConsoleEntry[];
  placeholder: string;
  /** SQL 这类长语句允许换行；Redis 命令保持单行 */
  multiline?: boolean;
  /** 右侧待批准面板 */
  pending?: ReactNode;
  /** 提交一行输入；返回 false 表示当前不可提交 */
  onSubmit: (input: string) => boolean;
  /** 输出区上方的工具条（会话切换 / 新建 / 结束） */
  header?: ReactNode;
  footer?: ReactNode;
  disabledHint?: string;
}) {
  const [input, setInput] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [historyIndex, setHistoryIndex] = useState(-1);
  const scrollRef = useRef<HTMLDivElement>(null);

  // 新条目出现时自动滚到底部
  useEffect(() => {
    const node = scrollRef.current;
    if (node) node.scrollTop = node.scrollHeight;
  }, [entries.length]);

  const ready = phase.kind === "ready";

  function submit() {
    const value = input.trim();
    if (!value || !ready) return;
    if (!onSubmit(value)) return;
    setHistory((previous) => [...previous, value]);
    setHistoryIndex(-1);
    setInput("");
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    // 多行模式（SQL）：Enter 换行，Ctrl/Cmd+Enter 执行——写长语句时不会被误提交。
    // 单行模式（Redis 命令）：Enter 直接执行。两种模式都可以点「执行」按钮。
    if (event.key === "Enter") {
      const wantsRun = multiline ? event.ctrlKey || event.metaKey : !event.shiftKey;
      if (wantsRun) {
        event.preventDefault();
        submit();
      }
      return;
    }
    // 光标不在首/末行时把 ↑↓ 留给文本编辑，只在边界翻历史
    if (event.key === "ArrowUp" && history.length > 0) {
      event.preventDefault();
      const next = historyIndex < 0 ? history.length - 1 : Math.max(0, historyIndex - 1);
      setHistoryIndex(next);
      setInput(history[next]);
      return;
    }
    if (event.key === "ArrowDown" && historyIndex >= 0) {
      event.preventDefault();
      const next = historyIndex + 1;
      if (next >= history.length) {
        setHistoryIndex(-1);
        setInput("");
      } else {
        setHistoryIndex(next);
        setInput(history[next]);
      }
    }
  }

  return (
    <DrawerishLayout pending={pending}>
      <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-3">
        {header}
        {disabledHint && ready && (
          <Notice status="warning" title="CLI 无法操作该连接">
            {disabledHint}
          </Notice>
        )}
        {/* 接入 / 结束 / 出错等状态写在输出框里面，不在标签栏和输出框之间插行——插行会把输出框推下去再弹回来，看起来像闪 */}
        <div
          ref={scrollRef}
          className="console-dark min-h-[55vh] flex-1 space-y-3 overflow-auto rounded-xl bg-[#0b0f19] p-3 font-mono text-xs leading-5 text-[#e5e7eb]"
          aria-label="控制台输出"
        >
          {phase.kind === "connecting" && (
            <p className="flex items-center gap-2 text-[#9ca3af]">
              <Spinner size="sm" color="current" /> 正在接入控制台…
            </p>
          )}
          {phase.kind === "closed" && <p className="text-[#fbbf24]">控制台会话已结束：{phase.reason}</p>}
          {phase.kind === "error" && <p className="text-[#f87171]">无法接入控制台：{phase.message}</p>}
          {phase.kind === "idle" && (
            <p className="text-[#9ca3af]">还没有会话。点「新建」开始；会话会一直保留到你主动结束或闲置一小时。</p>
          )}
          {ready && entries.length === 0 && (
            <p className="text-[#9ca3af]">在下面输入命令，或让 AI 通过 CLI 操作——它的命令也会出现在这里。</p>
          )}
          {entries.map((entry) => (
            <div key={entry.key} className="space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[#22d3ee]">›</span>
                <span className="break-all whitespace-pre-wrap">{entry.command}</span>
                {entry.source !== "web" && (
                  <Chip size="sm" variant="soft" color={sourceLabels[entry.source].color}>
                    <Chip.Label>{sourceLabels[entry.source].label}</Chip.Label>
                  </Chip>
                )}
                {entry.meta}
              </div>
              {entry.error && <p className="break-all whitespace-pre-wrap text-[#f87171]">{entry.error}</p>}
              {entry.note && <p className="break-all whitespace-pre-wrap text-[#fbbf24]">{entry.note}</p>}
              {entry.result}
            </div>
          ))}
        </div>
        <div className="flex items-end gap-2">
          <textarea
            value={input}
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={onKeyDown}
            rows={multiline ? 3 : 1}
            placeholder={ready ? placeholder : phase.kind === "idle" ? "先新建或选择一个会话" : "等待控制台就绪…"}
            disabled={!ready}
            aria-label="命令输入"
            className="min-w-0 flex-1 resize-none rounded-xl border border-separator bg-surface p-3 font-mono text-sm outline-none focus:border-accent disabled:opacity-50"
          />
          <Button isDisabled={!ready || input.trim() === ""} onPress={submit}>
            <Play size={16} aria-hidden="true" />
            执行
          </Button>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
          <span>{multiline ? "Ctrl+Enter 执行，Enter 换行" : "Enter 执行"}，↑↓ 翻历史</span>
          {footer}
        </div>
      </div>
    </DrawerishLayout>
  );
}

/** 主体 + 右侧待批准面板的两栏布局（与 SSH 终端抽屉一致） */
function DrawerishLayout({ pending, children }: { pending?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-col gap-3 lg:flex-row">
      {children}
      {pending && <aside className="flex w-full shrink-0 flex-col gap-3 overflow-y-auto lg:w-80">{pending}</aside>}
    </div>
  );
}

/** 侧栏里没有待批项时的占位说明 */
export function NoPendingHint({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-xl border border-separator bg-surface-secondary p-3 text-xs leading-5 text-muted">
      {children}
    </p>
  );
}

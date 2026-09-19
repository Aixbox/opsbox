/**
 * 自定义标题栏（主窗口 Frameless 后替代系统标题栏）。
 *
 * 布局：左侧拖拽区（应用图标 + 标题），右侧应用动作按钮 + Windows 风格窗口控制。
 * 拖拽：拖拽区带 `--wails-draggable: drag`，Wails 运行时在 mousedown（e.detail===1）
 * 时识别该 CSS 变量并进入原生 HTCAPTION 拖拽，因此支持 Aero Snap（拖到屏幕边缘分屏），
 * 最大化状态下拖动会自动还原窗口；双击不触发拖拽，可安全用作「双击切换最大化」。
 * 关闭：走 Go 绑定 WindowClose → OnBeforeClose 钩子，仍按设置页「关闭窗口时」
 * 决定隐藏到托盘还是退出。
 */
import { Button } from "@heroui/react";
import { Copy, Minus, Settings2, Square, TerminalSquare, X } from "lucide-react";
import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { desktopApi } from "~/lib/api/desktop";

const dragAreaStyle = { "--wails-draggable": "drag" } as CSSProperties;

export function TitleBar({ onOpenSettings, onOpenCli }: { onOpenSettings: () => void; onOpenCli: () => void }) {
  const [maximised, setMaximised] = useState(false);

  useEffect(() => {
    let alive = true;
    const sync = () => {
      desktopApi
        .isWindowMaximised()
        .then((value) => {
          if (alive) setMaximised(value);
        })
        .catch(() => {});
    };
    sync();
    // 最大化/还原、Aero Snap、拖动还原都会改变视口尺寸，借此同步按钮图标。
    window.addEventListener("resize", sync);
    return () => {
      alive = false;
      window.removeEventListener("resize", sync);
    };
  }, []);

  const toggleMaximise = () => {
    void desktopApi.toggleWindowMaximise();
  };

  return (
    <header className="flex h-11 shrink-0 select-none items-stretch border-b border-separator">
      {/* 拖拽区只覆盖标题与空白，动作/控制按钮放在外面，避免点击被原生拖拽吞掉 */}
      <div
        className="flex min-w-0 flex-1 items-center gap-2.5 pl-4"
        style={dragAreaStyle}
        onDoubleClick={toggleMaximise}
      >
        <img
          src="/app-icon.png"
          alt=""
          aria-hidden="true"
          draggable={false}
          className="size-5 rounded-md"
        />
        <span className="text-sm font-semibold tracking-tight">opsbox</span>
        <span className="min-w-0 truncate text-xs text-muted">本地运维工具箱 · SSH / 数据库 / Redis</span>
      </div>
      <div className="flex shrink-0 items-center gap-1.5 pr-2">
        <Button size="sm" variant="secondary" onPress={onOpenSettings}>
          <Settings2 size={16} aria-hidden="true" />
          设置
        </Button>
        <Button size="sm" variant="secondary" onPress={onOpenCli}>
          <TerminalSquare size={16} aria-hidden="true" />
          AI CLI
        </Button>
      </div>
      <div className="flex shrink-0 items-stretch">
        <CaptionButton label="最小化" onClick={() => void desktopApi.windowMinimise()}>
          <Minus size={16} strokeWidth={1.5} />
        </CaptionButton>
        <CaptionButton label={maximised ? "向下还原" : "最大化"} onClick={toggleMaximise}>
          {maximised ? <Copy size={13} strokeWidth={1.5} /> : <Square size={13} strokeWidth={1.5} />}
        </CaptionButton>
        <CaptionButton label="关闭" danger onClick={() => void desktopApi.closeWindow()}>
          <X size={17} strokeWidth={1.5} />
        </CaptionButton>
      </div>
    </header>
  );
}

// CaptionButton 是 Windows 标题栏风格的标题按钮：默认光标、悬停浅底，关闭键悬停变红（白图标）。
function CaptionButton({
  label,
  onClick,
  children,
  danger,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className={`flex w-11 cursor-default items-center justify-center text-muted outline-none transition-colors focus:outline-none ${
        danger ? "hover:bg-[#e81123] hover:text-white" : "hover:bg-foreground/5 hover:text-foreground"
      }`}
    >
      {children}
    </button>
  );
}

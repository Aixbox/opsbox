/**
 * 桌面端 Go 绑定访问层（window.go.main.App，仅 Wails 环境可用）。
 * 命名空间说明见 api-client.ts：绑定生成在 main 包下。
 */

export interface CliStatus {
  /** 主程序是否内嵌了 CLI 二进制（开发者本地 go build 时可能为 false） */
  bundled: boolean;
  /** 三个 CLI 是否已安装在用户目录 */
  installed: boolean;
  /** 安装目录是否已在用户 PATH */
  inPath: boolean;
  /** 内嵌 CLI 版本 */
  version: string;
  /** 安装目录 */
  installDir: string;
  /** PATH 中其他目录的同名 CLI（会遮蔽新命令，如旧平台 padmin） */
  conflicts?: string[];
}

export interface AppSettings {
  /** 点 X 关窗行为：tray = 隐藏到系统托盘（服务保持运行）；exit = 退出应用 */
  closeAction: "tray" | "exit";
  /** 是否已注册开机自启（HKCU Run，与托盘菜单同一开关） */
  autostart: boolean;
}

function bindings(): {
  CLIStatus?: () => Promise<CliStatus>;
  InstallCLIs?: () => Promise<CliStatus>;
  UninstallCLIs?: () => Promise<CliStatus>;
  TakeOverConflicts?: () => Promise<CliStatus>;
  DataDir?: () => Promise<string>;
  GetAppSettings?: () => Promise<AppSettings>;
  SetCloseAction?: (action: string) => Promise<void>;
  SetAutostart?: (enable: boolean) => Promise<void>;
} | undefined {
  return (window as unknown as { go?: { main?: { App?: Record<string, () => Promise<unknown>> } } })
    .go?.main?.App as never;
}

export const desktopApi = {
  available(): boolean {
    return Boolean(bindings());
  },
  async cliStatus(): Promise<CliStatus> {
    const call = bindings()?.CLIStatus;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    return call();
  },
  async installClis(): Promise<CliStatus> {
    const call = bindings()?.InstallCLIs;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    return call();
  },
  async uninstallClis(): Promise<CliStatus> {
    const call = bindings()?.UninstallCLIs;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    return call();
  },
  async takeOverConflicts(): Promise<CliStatus> {
    const call = bindings()?.TakeOverConflicts;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    return call();
  },
  async dataDir(): Promise<string> {
    const call = bindings()?.DataDir;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    return call();
  },
  async appSettings(): Promise<AppSettings> {
    const call = bindings()?.GetAppSettings;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    return call();
  },
  async setCloseAction(action: "tray" | "exit"): Promise<void> {
    const call = bindings()?.SetCloseAction;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    await call(action);
  },
  async setAutostart(enable: boolean): Promise<void> {
    const call = bindings()?.SetAutostart;
    if (!call) throw new Error("桌面绑定不可用（请在 opsbox 窗口内使用）");
    await call(enable);
  },
};

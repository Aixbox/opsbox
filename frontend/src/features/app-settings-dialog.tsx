import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Choice, Toggle } from "~/features/fields";
import { FormDialog, Notice, errorMessage } from "~/features/shared";
import { desktopApi, type AppSettings } from "~/lib/api/desktop";

const appSettingsKey = ["desktop", "app-settings"] as const;

/**
 * 应用设置：点 X 关窗行为（托盘 / 退出）与开机自启。
 * 由 Wails 绑定提供，保存即时生效；自启与托盘菜单是同一个注册表开关。
 */
export function AppSettingsDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const settings = useQuery({ queryKey: appSettingsKey, queryFn: () => desktopApi.appSettings() });
  const [closeAction, setCloseAction] = useState<AppSettings["closeAction"]>("tray");
  const [autostart, setAutostart] = useState(false);

  useEffect(() => {
    if (settings.data) {
      setCloseAction(settings.data.closeAction);
      setAutostart(settings.data.autostart);
    }
  }, [settings.data]);

  const save = useMutation({
    mutationFn: async () => {
      const current = settings.data;
      if (!current) return;
      // 只提交变化项：关窗行为写 config.json，自启写注册表（同时刷新托盘菜单）。
      if (current.closeAction !== closeAction) {
        await desktopApi.setCloseAction(closeAction);
      }
      if (current.autostart !== autostart) {
        await desktopApi.setAutostart(autostart);
      }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: appSettingsKey });
      onClose();
    },
  });

  return (
    <FormDialog
      title="应用设置"
      description="关窗行为与开机自启保存后即时生效；开机自启与托盘菜单里的开关是同一个注册表项。"
      onClose={onClose}
      onSubmit={() => save.mutateAsync()}
      dialogClassName="w-full sm:max-w-lg"
      submitDisabled={!settings.data || save.isPending}
    >
      {settings.isPending && <p className="text-sm text-muted">读取设置…</p>}
      {settings.isError && (
        <Notice status="danger" title="设置读取失败">
          {errorMessage(settings.error)}
        </Notice>
      )}
      {settings.data && (
        <>
          <Choice
            label="关闭窗口（点 X）时"
            value={closeAction}
            onChange={(value) => setCloseAction(value === "exit" ? "exit" : "tray")}
            options={[
              { value: "tray", label: "最小化到系统托盘（推荐）" },
              { value: "exit", label: "退出应用" },
            ]}
            description="最小化到托盘时本地服务与 AI CLI 保持运行，托盘单击可唤回窗口；退出则停止服务与 CLI。托盘「退出」始终直接退出。"
          />
          <Toggle
            label="开机自启"
            selected={autostart}
            onChange={setAutostart}
            description="登录 Windows 后自动在后台运行 opsbox（写入当前用户注册表 Run 项，不弹窗口时驻留托盘）。"
          />
        </>
      )}
      {save.isError && (
        <Notice status="danger" title="保存失败">
          {errorMessage(save.error)}
        </Notice>
      )}
    </FormDialog>
  );
}

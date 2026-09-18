import { Button, Chip, Modal, Spinner } from "@heroui/react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TerminalSquare } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDialog,
  Notice,
  errorMessage,
  type Confirmation,
} from "~/features/shared";
import { desktopApi, type CliStatus } from "~/lib/api/desktop";

const cliKey = ["desktop", "cli-status"] as const;

/** AI CLI 面板：内嵌 CLI 的安装状态、一键安装/卸载、旧版同名命令冲突提示。 */
export function CliDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const status = useQuery({ queryKey: cliKey, queryFn: () => desktopApi.cliStatus() });

  const install = useMutation({
    mutationFn: () => desktopApi.installClis(),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: cliKey }),
  });
  const uninstall = useMutation({
    mutationFn: () => desktopApi.uninstallClis(),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: cliKey }),
  });
  const takeOver = useMutation({
    mutationFn: () => desktopApi.takeOverConflicts(),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: cliKey }),
  });

  const busy = install.isPending || uninstall.isPending || takeOver.isPending;
  const data: CliStatus | undefined = status.data;

  return (
    <>
      <Modal.Backdrop
        isOpen
        onOpenChange={(open) => {
          if (!open) onClose();
        }}
      >
        <Modal.Container scroll="inside">
          <Modal.Dialog className="w-full sm:max-w-2xl">
            <Modal.CloseTrigger />
            <Modal.Header>
              <Modal.Heading>
                <span className="inline-flex items-center gap-2">
                  <TerminalSquare size={18} aria-hidden="true" />
                  AI CLI（sshctl / sqlctl / redisctl）
                </span>
              </Modal.Heading>
            </Modal.Header>
            <Modal.Body className="space-y-4">
              {status.isPending && (
                <div className="flex items-center gap-2 py-6 text-sm text-muted">
                  <Spinner size="sm" /> 读取安装状态…
                </div>
              )}
              {status.isError && (
                <Notice status="danger" title="状态读取失败">
                  {errorMessage(status.error)}
                </Notice>
              )}
              {data && (
                <>
                  <div className="flex flex-wrap items-center gap-2">
                    <Chip size="sm" variant="soft" color={data.installed ? "success" : "warning"}>
                      <Chip.Label>{data.installed ? "已安装" : "未安装"}</Chip.Label>
                    </Chip>
                    <Chip size="sm" variant="soft" color={data.inPath ? "success" : "warning"}>
                      <Chip.Label>{data.inPath ? "已在 PATH" : "不在 PATH"}</Chip.Label>
                    </Chip>
                    <Chip size="sm" variant="soft" color="default">
                      <Chip.Label>CLI 版本 {data.version}</Chip.Label>
                    </Chip>
                  </div>
                  <p className="text-sm leading-6 text-muted">
                    安装目录：<span className="font-mono text-xs">{data.installDir}</span>
                    。安装后<b>重开一个终端</b>即可在任意目录使用；重复点击安装可随应用升级覆盖更新。
                  </p>
                  {!data.bundled && (
                    <Notice status="warning" title="当前程序未内嵌 CLI">
                      开发者构建：请先运行 scripts/build-all.ps1 重新出包，再回到此页安装。
                    </Notice>
                  )}
                  {data.conflicts && data.conflicts.length > 0 && (
                    <Notice status="warning" title="发现同名命令（PATH 冲突）">
                      <div className="space-y-2">
                        <p>以下目录里存在同名 CLI（如旧平台的 sshctl），且排在安装目录之前，命令行里会优先命中它们：</p>
                        {data.conflicts.map((dir) => (
                          <p key={dir} className="font-mono text-xs">
                            {dir}
                          </p>
                        ))}
                        <div className="flex items-center gap-2">
                          <Button
                            size="sm"
                            variant="secondary"
                            isDisabled={busy}
                            onPress={() =>
                              setConfirmation({
                                title: "移除旧版同名命令？",
                                description:
                                  "将删除上述目录里的 sshctl / sqlctl / redisctl（旧平台的 CLI），目录因此清空则一并移除，并从用户 PATH 去掉这些目录。目录里的其他文件不受影响。",
                                danger: true,
                                label: "移除旧版命令",
                                action: () => takeOver.mutateAsync(),
                              })
                            }
                          >
                            移除旧版命令
                          </Button>
                          <span className="text-xs text-muted">或自行卸载旧平台 CLI 后重开终端。</span>
                        </div>
                      </div>
                    </Notice>
                  )}
                  <div className="rounded-xl border border-separator bg-surface-secondary p-3 text-xs leading-5 text-muted">
                    <p className="font-medium text-foreground">AI 使用要点（完整指南见仓库 docs/cli.md）</p>
                    <ul className="mt-1 list-disc space-y-1 pl-4">
                      <li>会话即授权：只有你在窗口里打开的终端 / 控制台能被 CLI 操作。</li>
                      <li>需审批的写操作返回退出码 5 + 预检令牌：AI 先向你展示命令，确认后带 --confirm-token 重提，进入待批准队列。</li>
                      <li>退出码：0 成功；1 远端失败；2 被拦截/拒绝；3 超时；4 服务不可达；5 需审批确认。</li>
                    </ul>
                  </div>
                </>
              )}
              {install.isError && (
                <Notice status="danger" title="安装失败">
                  {errorMessage(install.error)}
                </Notice>
              )}
              {uninstall.isError && (
                <Notice status="danger" title="卸载失败">
                  {errorMessage(uninstall.error)}
                </Notice>
              )}
              {takeOver.isError && (
                <Notice status="danger" title="移除旧版命令失败">
                  {errorMessage(takeOver.error)}
                </Notice>
              )}
            </Modal.Body>
            <Modal.Footer>
              {data?.installed && (
                <Button
                  variant="tertiary"
                  isDisabled={busy}
                  onPress={() =>
                    setConfirmation({
                      title: "卸载 CLI？",
                      description: "将删除安装目录并从 PATH 移除 sshctl / sqlctl / redisctl。已打开的终端需要重开。",
                      danger: true,
                      label: "卸载",
                      action: () => uninstall.mutateAsync(),
                    })
                  }
                >
                  卸载
                </Button>
              )}
              <Button onPress={() => install.mutate()} isDisabled={Boolean(data && !data.bundled) || busy}>
                {install.isPending && <Spinner size="sm" />}
                {data?.installed ? "覆盖安装 / 升级" : "一键安装"}
              </Button>
            </Modal.Footer>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
      <ConfirmDialog confirmation={confirmation} onClose={() => setConfirmation(null)} />
    </>
  );
}

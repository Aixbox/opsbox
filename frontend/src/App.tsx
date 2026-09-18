import { Button } from "@heroui/react";
import { useQuery } from "@tanstack/react-query";
import { Plus, Settings2 } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDialog,
  DataTable,
  PageHeader,
  QueryError,
  RefreshButton,
  StatusChip,
  dateTime,
  type Confirmation,
} from "~/features/shared";
import { useSshActions } from "~/features/ssh/actions";
import { ConnectionDrawer } from "~/features/ssh/connection-form";
import { ExecDialog } from "~/features/ssh/exec-dialog";
import { PendingPanel } from "~/features/ssh/pending-panel";
import { SettingsDialog } from "~/features/ssh/settings-dialog";
import { PolicyChip, sshKey } from "~/features/ssh/shared";
import { TerminalDrawer } from "~/features/ssh/terminal-drawer";
import { sshApi, type SshConnection } from "~/lib/api/ssh";

export default function App() {
  const connections = useQuery({
    queryKey: [...sshKey, "connections"],
    queryFn: ({ signal }) => sshApi.connections(signal),
    refetchInterval: 15_000,
  });
  const pending = useQuery({
    queryKey: [...sshKey, "pending"],
    queryFn: ({ signal }) => sshApi.pending(signal),
    refetchInterval: 5_000,
  });
  const settings = useQuery({
    queryKey: [...sshKey, "settings"],
    queryFn: ({ signal }) => sshApi.settings(signal),
  });
  const actions = useSshActions();
  const [editor, setEditor] = useState<SshConnection | "new" | null>(null);
  const [terminal, setTerminal] = useState<SshConnection | null>(null);
  const [exec, setExec] = useState<SshConnection | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const pendingCount = pending.data?.items.length ?? 0;

  function rowAction(row: SshConnection, key: string) {
    switch (key) {
      case "terminal":
        setTerminal(row);
        break;
      case "exec":
        setExec(row);
        break;
      case "test":
        void actions.run(async () => {
          const result = await sshApi.testConnection(row.id);
          return result;
        }, `${row.name} 连接成功，主机指纹已记录`);
        break;
      case "edit":
        setEditor(row);
        break;
      case "delete":
        setConfirmation({
          title: `删除连接 ${row.name}`,
          description: "相关执行日志会一并删除，进行中的会话会断开。",
          danger: true,
          label: "删除",
          action: () => actions.run(() => sshApi.deleteConnection(row.id), "连接已删除"),
        });
        break;
    }
  }

  return (
    <div className="mx-auto min-h-screen max-w-6xl px-6 py-8">
      <div className="min-w-0 space-y-6">
        <PageHeader
          title="SSH 管理"
          description="本地托管的 SSH 运维工具：凭证加密落盘，AI 通过本机 CLI 执行命令、传输文件；每条操作落审计，可按连接切换审批策略。"
        >
          <RefreshButton
            pending={connections.isFetching}
            onPress={() => {
              void connections.refetch();
              void pending.refetch();
            }}
          />
          <Button variant="secondary" isDisabled={!settings.data} onPress={() => setShowSettings(true)}>
            <Settings2 size={16} aria-hidden="true" />
            设置
          </Button>
          <Button onPress={() => setEditor("new")}>
            <Plus size={16} aria-hidden="true" />
            添加连接
          </Button>
        </PageHeader>
        {actions.feedback}
        <QueryError
          error={connections.error}
          retry={() => {
            void connections.refetch();
          }}
        />
        {pendingCount > 0 && (
          <PendingPanel
            items={pending.data?.items ?? []}
            onChanged={async () => {
              await pending.refetch();
              await actions.refresh();
            }}
          />
        )}
        <DataTable
          label="SSH 连接"
          rows={connections.data?.items ?? []}
          loading={connections.isPending}
          failed={connections.isError}
          empty="还没有连接。添加一台服务器后，即可在网页终端操作它；本机 CLI 落地后 AI 也能直接使用。"
          columns={[
            {
              key: "name",
              label: "连接",
              render: (row) => (
                <div className="min-w-0">
                  <p className="font-medium">{row.name}</p>
                  <p className="mt-1 truncate text-xs text-muted">
                    {row.username}@{row.host}:{row.port} · {row.authType === "key" ? "私钥" : "密码"}
                  </p>
                  {row.remark && <p className="mt-1 line-clamp-1 text-xs text-muted">{row.remark}</p>}
                </div>
              ),
            },
            { key: "policy", label: "策略", render: (row) => <PolicyChip policy={row.execPolicy} /> },
            {
              key: "status",
              label: "状态",
              render: (row) => <StatusChip status={row.enabled ? "active" : "disabled"} />,
            },
            {
              key: "hostKey",
              label: "主机指纹",
              render: (row) => (
                <span className="font-mono text-xs text-muted" title={row.hostKey || undefined}>
                  {row.hostKey ? `${row.hostKey.slice(0, 20)}…` : "未记录（首连时记录）"}
                </span>
              ),
            },
            {
              key: "updated",
              label: "更新时间",
              render: (row) => <span className="text-sm">{dateTime(row.updatedAt)}</span>,
            },
            {
              key: "actions",
              label: "操作",
              render: (row) => (
                <div className="flex flex-wrap items-center gap-1">
                  <Button
                    size="sm"
                    variant="tertiary"
                    isDisabled={!row.enabled}
                    onPress={() => setTerminal(row)}
                  >
                    终端
                  </Button>
                  <Button
                    size="sm"
                    variant="tertiary"
                    isDisabled={!row.enabled}
                    onPress={() => rowAction(row, "exec")}
                  >
                    执行命令
                  </Button>
                  <Button size="sm" variant="tertiary" onPress={() => rowAction(row, "test")}>
                    测试连接
                  </Button>
                  <Button size="sm" variant="tertiary" onPress={() => rowAction(row, "edit")}>
                    编辑
                  </Button>
                  <Button size="sm" variant="tertiary" onPress={() => rowAction(row, "delete")}>
                    删除
                  </Button>
                </div>
              ),
            },
          ]}
        />
        <section className="rounded-2xl border border-separator bg-surface-secondary p-4 text-sm leading-6 text-muted">
          <p className="font-medium text-foreground">使用说明</p>
          <ol className="mt-1 list-decimal space-y-2 pl-5">
            <li>
              在上方「添加连接」配置服务器（密码或私钥均以 AES-256-GCM 加密保存在本机数据库，不会明文落盘）；
            </li>
            <li>
              点「终端」打开网页终端；命令执行记录可在「待批准操作」与本机数据库中追溯；
            </li>
            <li>逐条审批策略下的操作会出现在页面顶部的「待批准操作」，批准后才真正执行。</li>
          </ol>
          {pendingCount > 0 && <p className="mt-2 text-xs">待批准操作：{pendingCount} 条</p>}
        </section>
        {editor && (
          <ConnectionDrawer
            connection={editor === "new" ? undefined : editor}
            onClose={() => setEditor(null)}
            onSaved={() => actions.refresh()}
          />
        )}
        {terminal && <TerminalDrawer connection={terminal} onClose={() => setTerminal(null)} />}
        {exec && <ExecDialog connection={exec} onClose={() => setExec(null)} />}
        {showSettings && settings.data && (
          <SettingsDialog
            settings={settings.data}
            onClose={() => setShowSettings(false)}
            onSaved={() => actions.refresh()}
          />
        )}
        <ConfirmDialog confirmation={confirmation} onClose={() => setConfirmation(null)} />
      </div>
    </div>
  );
}

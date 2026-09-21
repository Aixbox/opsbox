import { Button } from "@heroui/react";
import { useQuery } from "@tanstack/react-query";
import { Plus, ScanSearch, Settings2, Terminal } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDialog,
  DataTable,
  PageHeader,
  QueryError,
  RefreshButton,
  RowTestButton,
  StatusChip,
  AgentPrompt,
  dateTime,
  type Confirmation,
} from "~/features/shared";
import { useRedisActions } from "~/features/redis/actions";
import { ConnectionDrawer } from "~/features/redis/connection-form";
import { ConsoleDrawer } from "~/features/redis/console-drawer";
import { PendingPanel } from "~/features/redis/pending-panel";
import { ScanDialog } from "~/features/redis/scan-dialog";
import { SettingsDialog } from "~/features/redis/settings-dialog";
import { PolicyChip, redisKey } from "~/features/redis/shared";
import { redisApi, type RedisConnection } from "~/lib/api/redis";

export default function RedisPage() {
  const connections = useQuery({
    queryKey: [...redisKey, "connections"],
    queryFn: ({ signal }) => redisApi.connections(signal),
    refetchInterval: 15_000,
  });
  const pending = useQuery({
    queryKey: [...redisKey, "pending"],
    queryFn: ({ signal }) => redisApi.pending(signal),
    refetchInterval: 5_000,
  });
  const settings = useQuery({
    queryKey: [...redisKey, "settings"],
    queryFn: ({ signal }) => redisApi.settings(signal),
  });
  const actions = useRedisActions();
  const [editor, setEditor] = useState<RedisConnection | "new" | null>(null);
  const [scan, setScan] = useState<RedisConnection | null>(null);
  const [console_, setConsole] = useState<RedisConnection | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const pendingCount = pending.data?.items.length ?? 0;

  return (
    <div className="min-w-0 space-y-6">
      <PageHeader
        title="Redis 管理"
        description="本地托管的 Redis / Valkey 运维工具：写命令按连接策略审批，高危命令黑名单拦截，命令与回复全程落审计。"
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
        label="Redis 连接"
        rows={connections.data?.items ?? []}
        loading={connections.isPending}
        failed={connections.isError}
        empty="还没有连接。添加一个实例后，即可在控制台与扫描面板里操作它。"
        columns={[
          {
            key: "name",
            label: "连接",
            render: (row) => (
              <div className="min-w-0">
                <p className="font-medium">{row.name}</p>
                <p className="mt-1 truncate text-xs text-muted">
                  {row.host}:{row.port} · db{row.db}
                  {row.tls ? " · TLS" : ""}
                  {row.hasPassword ? " · 密码" : ""}
                </p>
                {row.remark && <p className="mt-1 line-clamp-1 text-xs text-muted">{row.remark}</p>}
              </div>
            ),
          },
          { key: "policy", label: "写策略", render: (row) => <PolicyChip policy={row.writePolicy} /> },
          {
            key: "status",
            label: "状态",
            render: (row) => <StatusChip status={row.enabled ? "active" : "disabled"} />,
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
                <Button size="sm" variant="tertiary" isDisabled={!row.enabled} onPress={() => setConsole(row)}>
                  <Terminal size={14} aria-hidden="true" />
                  控制台
                </Button>
                <Button size="sm" variant="tertiary" isDisabled={!row.enabled} onPress={() => setScan(row)}>
                  <ScanSearch size={14} aria-hidden="true" />
                  扫描
                </Button>
                <RowTestButton
                  run={actions.run}
                  action={() => redisApi.testConnection(row.id)}
                  successMessage={`${row.name} 连接成功`}
                />
                <Button size="sm" variant="tertiary" onPress={() => setEditor(row)}>
                  编辑
                </Button>
                <Button
                  size="sm"
                  variant="tertiary"
                  onPress={() =>
                    setConfirmation({
                      title: `删除连接 ${row.name}`,
                      description: "相关命令日志会一并删除，池中的连接立即关闭。",
                      danger: true,
                      label: "删除",
                      action: () => actions.run(() => redisApi.deleteConnection(row.id), "连接已删除"),
                    })
                  }
                >
                  删除
                </Button>
              </div>
            ),
          },
        ]}
      />
      <section className="rounded-2xl border border-separator bg-surface-secondary p-4 text-sm leading-6 text-muted">
        <p className="font-medium text-foreground">使用说明（人 + AI Agent 协作）</p>
        <ol className="mt-1 list-decimal space-y-2 pl-5">
          <li>
            在上方「添加连接」配置实例（密码以 AES-256-GCM 加密保存在本机数据库，不会明文落盘，也永不下发给 AI Agent）；
          </li>
          <li>
            点「控制台」打开会话——会话即授权：AI Agent 只能操作已打开控制台的连接；人可在此交互执行命令，「扫描」用安全 SCAN 摸 key 分布；
          </li>
          <li>
            右上角「AI CLI」一键安装命令行工具（装完重开终端），让 AI Agent 通过 redisctl 操控，如：
            redisctl scan 连接名 "user:*"；完整用法与审批协议见仓库内 docs/cli.md；
          </li>
          <li>
            写命令审批策略下，Agent 的写命令要先拿预检令牌并征得你确认，重提后进入页面顶部「待批准命令」，你批准后才真正执行（CLI 无法自批自审）；
          </li>
          <li>所有命令（含 Agent 发起的）都会在「日志」页回溯，回复加密落库、按天保留。</li>
        </ol>
        <AgentPrompt
          text={`redisctl 是本机已安装的命令行工具（不是 MCP），用于在我授权的 Redis 连接上执行命令。先执行 redisctl sessions list 查看可用连接，只操作列出的连接。其余用法执行 redisctl --help 查看。`}
        />
      </section>
      {editor && (
        <ConnectionDrawer
          connection={editor === "new" ? undefined : editor}
          onClose={() => setEditor(null)}
          onSaved={() => actions.refresh()}
        />
      )}
      {scan && <ScanDialog connection={scan} onClose={() => setScan(null)} />}
      {console_ && <ConsoleDrawer connection={console_} onClose={() => setConsole(null)} />}
      {showSettings && settings.data && (
        <SettingsDialog
          settings={settings.data}
          onClose={() => setShowSettings(false)}
          onSaved={() => actions.refresh()}
        />
      )}
      <ConfirmDialog confirmation={confirmation} onClose={() => setConfirmation(null)} />
    </div>
  );
}

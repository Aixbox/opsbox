import { Button } from "@heroui/react";
import { useQuery } from "@tanstack/react-query";
import { Plus, Settings2, Terminal } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDialog,
  DataTable,
  PageHeader,
  QueryError,
  RefreshButton,
  StatusChip,
  AgentPrompt,
  dateTime,
  type Confirmation,
} from "~/features/shared";
import { useSqlActions } from "~/features/sql/actions";
import { ConnectionDrawer } from "~/features/sql/connection-form";
import { ConsoleDrawer } from "~/features/sql/console-drawer";
import { PendingPanel } from "~/features/sql/pending-panel";
import { QueryDialog } from "~/features/sql/query-dialog";
import { SettingsDialog } from "~/features/sql/settings-dialog";
import { PolicyChip, engineLabels, sqlKey } from "~/features/sql/shared";
import { sqlApi, type SqlConnection } from "~/lib/api/sql";

export default function SqlPage() {
  const connections = useQuery({
    queryKey: [...sqlKey, "connections"],
    queryFn: ({ signal }) => sqlApi.connections(signal),
    refetchInterval: 15_000,
  });
  const pending = useQuery({
    queryKey: [...sqlKey, "pending"],
    queryFn: ({ signal }) => sqlApi.pending(signal),
    refetchInterval: 5_000,
  });
  const settings = useQuery({
    queryKey: [...sqlKey, "settings"],
    queryFn: ({ signal }) => sqlApi.settings(signal),
  });
  const actions = useSqlActions();
  const [editor, setEditor] = useState<SqlConnection | "new" | null>(null);
  const [query, setQuery] = useState<SqlConnection | null>(null);
  const [console_, setConsole] = useState<SqlConnection | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const pendingCount = pending.data?.items.length ?? 0;

  return (
    <div className="min-w-0 space-y-6">
      <PageHeader
        title="数据库"
        description="本地托管的 MySQL / PostgreSQL 运维工具：凭证加密落盘，SQL 按连接的三档写策略（只读 / 审批 / 放行）执行，黑名单与审计全程生效。"
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
        label="数据库连接"
        rows={connections.data?.items ?? []}
        loading={connections.isPending}
        failed={connections.isError}
        empty="还没有连接。添加一个 MySQL / PostgreSQL 实例后，即可在控制台与查询面板里操作它。"
        columns={[
          {
            key: "name",
            label: "连接",
            render: (row) => (
              <div className="min-w-0">
                <p className="font-medium">{row.name}</p>
                <p className="mt-1 truncate text-xs text-muted">
                  {engineLabels[row.engine] ?? row.engine} · {row.username}@{row.host}:{row.port} · {row.database}
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
                <Button size="sm" variant="tertiary" isDisabled={!row.enabled} onPress={() => setQuery(row)}>
                  执行 SQL
                </Button>
                <Button size="sm" variant="tertiary" onPress={() => void actions.run(async () => sqlApi.testConnection(row.id), `${row.name} 连接成功`)}>
                  测试
                </Button>
                <Button size="sm" variant="tertiary" onPress={() => setEditor(row)}>
                  编辑
                </Button>
                <Button
                  size="sm"
                  variant="tertiary"
                  onPress={() =>
                    setConfirmation({
                      title: `删除连接 ${row.name}`,
                      description: "相关查询日志会一并删除，进行中的查询会中断。",
                      danger: true,
                      label: "删除",
                      action: () => actions.run(() => sqlApi.deleteConnection(row.id), "连接已删除"),
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
            点「控制台」打开会话——会话即授权：AI Agent 只能操作已打开控制台的连接；人可在此交互查询，或点「执行 SQL」跑整段脚本；
          </li>
          <li>
            右上角「AI CLI」一键安装命令行工具（装完重开终端），让 AI Agent 通过 sqlctl 查数据，如：
            sqlctl query 连接名 --json -- "SELECT ... LIMIT 20"；完整用法与审批协议见仓库内 docs/cli.md；
          </li>
          <li>
            confirm 写策略下，Agent 的写 SQL 要先拿预检令牌并征得你确认，重提后进入页面顶部「待批准写操作」，你批准后才真正执行（CLI 无法自批自审）；
          </li>
          <li>所有查询（含 Agent 发起的）都会在「日志」页回溯，结果加密落库、按天保留。</li>
        </ol>
        <AgentPrompt
          text={`sqlctl 是本机已安装的命令行工具（不是 MCP），用于在我授权的 MySQL/PostgreSQL 连接上执行 SQL。先执行 sqlctl sessions list 查看可用连接，只操作列出的连接。其余用法执行 sqlctl --help 查看。`}
        />
      </section>
      {editor && (
        <ConnectionDrawer
          connection={editor === "new" ? undefined : editor}
          onClose={() => setEditor(null)}
          onSaved={() => actions.refresh()}
        />
      )}
      {query && <QueryDialog connection={query} onClose={() => setQuery(null)} />}
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

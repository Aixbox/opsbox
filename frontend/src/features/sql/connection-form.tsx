import { Button, Drawer, Spinner } from "@heroui/react";
import { useId, useState } from "react";
import { Choice, Field, NumericField, TextFieldArea, Toggle } from "~/features/fields";
import { Notice, TestConnectionButton, errorMessage } from "~/features/shared";
import { sqlApi, type SqlConnection, type SqlConnectionInput, type SqlEngine } from "~/lib/api/sql";
import { engineLabels, policyLabels } from "./shared";

function fresh(): SqlConnectionInput {
  return {
    name: "",
    engine: "mysql",
    host: "",
    port: 3306,
    username: "root",
    database: "",
    params: "",
    writePolicy: "confirm",
    enabled: true,
    remark: "",
  };
}

function fromConnection(connection: SqlConnection): SqlConnectionInput {
  return {
    name: connection.name,
    engine: connection.engine,
    host: connection.host,
    port: connection.port,
    username: connection.username,
    database: connection.database,
    params: connection.params,
    writePolicy: connection.writePolicy,
    enabled: connection.enabled,
    remark: connection.remark,
  };
}

/** 连接新建 / 编辑抽屉。密码不回显：编辑时留空保持原值（PostgreSQL 留空表示免密）。 */
export function ConnectionDrawer({
  connection,
  onClose,
  onSaved,
}: {
  connection?: SqlConnection;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const formId = useId();
  const [input, setInput] = useState<SqlConnectionInput>(() => (connection ? fromConnection(connection) : fresh()));
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const editing = Boolean(connection);
  const keepHint = editing ? "留空保留已保存的值。" : undefined;
  const isPostgres = input.engine === "postgres";

  async function submit() {
    if (pending) return;
    setPending(true);
    setError(undefined);
    try {
      const payload: SqlConnectionInput = { ...input };
      if (password.trim() || (!editing && !isPostgres)) payload.password = password;
      await sqlApi.saveConnection(payload, connection?.id);
      await onSaved();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setPending(false);
    }
  }

  /** 用表单当前值真实连一次：密码没填就交给后端按 fromId 回退已保存值（编辑场景） */
  function testConnection(): Promise<string> {
    const payload: SqlConnectionInput = { ...input };
    if (password.trim()) payload.password = password;
    return sqlApi
      .testTarget(payload, connection?.id)
      .then((result) => `已连通 ${engineLabels[result.engine] ?? result.engine} 数据库 ${result.database}`);
  }

  return (
    <Drawer.Backdrop
      isOpen
      isDismissable={!pending}
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-screen max-w-full sm:max-w-xl">
          <Drawer.CloseTrigger isDisabled={pending} />
          <Drawer.Header>
            <Drawer.Heading>{editing ? "编辑连接" : "添加数据库连接"}</Drawer.Heading>
            <p className="mt-2 text-sm leading-6 text-muted">
              密码加密保存在平台后端，CLI 与 AI 都拿不到明文；SQL 执行与审计全部在服务端完成。
            </p>
          </Drawer.Header>
          <Drawer.Body>
            <form
              id={formId}
              className="space-y-5"
              onSubmit={(event) => {
                event.preventDefault();
                void submit();
              }}
            >
              <fieldset disabled={pending} className="min-w-0 space-y-5">
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field
                    label="名称"
                    value={input.name}
                    onChange={(name) => setInput({ ...input, name })}
                    required
                    maxLength={100}
                    placeholder="prod-mysql"
                    description="CLI 用名称引用：sqlctl query <名称> -- …"
                  />
                  <Choice
                    label="引擎"
                    value={input.engine}
                    onChange={(engine) => {
                      const next = engine as SqlEngine;
                      setInput({
                        ...input,
                        engine: next,
                        port:
                          input.port === (isPostgres ? 5432 : 3306) ? (next === "postgres" ? 5432 : 3306) : input.port,
                      });
                    }}
                    options={Object.entries(engineLabels).map(([value, label]) => ({ value, label }))}
                  />
                </div>
                <div className="grid gap-4 sm:grid-cols-[1fr_160px]">
                  <Field
                    label="主机"
                    value={input.host}
                    onChange={(host) => setInput({ ...input, host })}
                    required
                    placeholder="10.0.0.8 或 db.example.com"
                  />
                  <NumericField
                    label="端口"
                    value={String(input.port)}
                    onChange={(port) => setInput({ ...input, port: Number(port) || (isPostgres ? 5432 : 3306) })}
                    min={1}
                    max={65535}
                    required
                  />
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field
                    label="用户名"
                    value={input.username}
                    onChange={(username) => setInput({ ...input, username })}
                    required
                    maxLength={64}
                  />
                  <Field
                    label="数据库"
                    value={input.database}
                    onChange={(database) => setInput({ ...input, database })}
                    required
                    maxLength={64}
                  />
                </div>
                <Field
                  label="密码"
                  type="password"
                  value={password}
                  onChange={setPassword}
                  required={(!editing || !connection?.hasPassword) && !isPostgres}
                  autoComplete="new-password"
                  description={isPostgres ? "PostgreSQL 可留空表示免密认证。" : keepHint}
                />
                <Choice
                  label="写策略"
                  value={input.writePolicy}
                  onChange={(writePolicy) =>
                    setInput({ ...input, writePolicy: writePolicy as SqlConnectionInput["writePolicy"] })
                  }
                  options={Object.entries(policyLabels).map(([value, label]) => ({ value, label }))}
                  description={
                    input.writePolicy === "readonly"
                      ? "写操作一律拒绝，适合接给只需要查数据的 AI。"
                      : input.writePolicy === "confirm"
                        ? "读直接执行；写操作（含 DDL）先进待批队列，Web 批准后才执行。"
                        : "读写都直接执行 + 全量审计；黑名单仍然生效。"
                  }
                />
                <TextFieldArea
                  label="扩展参数（JSON，可选）"
                  value={input.params}
                  onChange={(params) => setInput({ ...input, params })}
                  rows={2}
                  code
                  description={
                    isPostgres
                      ? '示例：{"sslmode":"require"}。可选键：sslmode / connect_timeout / application_name / search_path / timezone / statement_timeout'
                      : '示例：{"charset":"utf8mb4"}。可选键：charset / collation / tls / time_zone / interpolateParams / maxAllowedPacket'
                  }
                />
                <Field
                  label="备注"
                  value={input.remark}
                  onChange={(remark) => setInput({ ...input, remark })}
                  maxLength={500}
                />
                <Toggle label="启用" selected={input.enabled} onChange={(enabled) => setInput({ ...input, enabled })} />
                <TestConnectionButton
                  onTest={testConnection}
                  disabled={pending}
                  failureHint={
                    editing && !password.trim()
                      ? "密码留空：本次测试用的是已保存的密码。如果密码已变更，请先在密码栏填写再测。"
                      : undefined
                  }
                />
              </fieldset>
              {error && (
                <Notice status="danger" title="保存失败">
                  {error}
                </Notice>
              )}
            </form>
          </Drawer.Body>
          <Drawer.Footer>
            <Button variant="secondary" isDisabled={pending} onPress={onClose}>
              取消
            </Button>
            <Button type="submit" form={formId} isPending={pending}>
              {pending && <Spinner size="sm" color="current" />}
              保存
            </Button>
          </Drawer.Footer>
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  );
}

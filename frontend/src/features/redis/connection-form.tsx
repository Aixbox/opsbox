import { Button, Drawer, Spinner } from "@heroui/react";
import { useId, useState } from "react";
import { Choice, Field, NumericField, Toggle } from "~/features/fields";
import { Notice, TestConnectionButton, errorMessage } from "~/features/shared";
import { redisApi, type RedisConnection, type RedisConnectionInput } from "~/lib/api/redis";
import { policyLabels } from "./shared";

function fresh(): RedisConnectionInput {
  return {
    name: "",
    host: "",
    port: 6379,
    db: 0,
    tls: false,
    writePolicy: "confirm",
    enabled: true,
    remark: "",
  };
}

function fromConnection(connection: RedisConnection): RedisConnectionInput {
  return {
    name: connection.name,
    host: connection.host,
    port: connection.port,
    db: connection.db,
    tls: connection.tls,
    writePolicy: connection.writePolicy,
    enabled: connection.enabled,
    remark: connection.remark,
  };
}

/** 连接新建 / 编辑抽屉。密码不回显：编辑时留空保持原值，填空串清除。 */
export function ConnectionDrawer({
  connection,
  onClose,
  onSaved,
}: {
  connection?: RedisConnection;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const formId = useId();
  const [input, setInput] = useState<RedisConnectionInput>(() => (connection ? fromConnection(connection) : fresh()));
  const [password, setPassword] = useState("");
  const [clearPassword, setClearPassword] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const editing = Boolean(connection);

  async function submit() {
    if (pending) return;
    setPending(true);
    setError(undefined);
    try {
      const payload: RedisConnectionInput = { ...input };
      if (clearPassword) {
        payload.password = "";
      } else if (password.trim() || !editing) {
        payload.password = password;
      }
      await redisApi.saveConnection(payload, connection?.id);
      await onSaved();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setPending(false);
    }
  }

  /** 用表单当前值真实 PING 一次：密码没填交给后端按 fromId 回退；勾了「清除密码」按无密码实例测 */
  function testConnection(): Promise<string> {
    const payload: RedisConnectionInput = { ...input };
    if (clearPassword) {
      payload.password = "";
    } else if (password.trim()) {
      payload.password = password;
    }
    return redisApi.testTarget(payload, connection?.id).then((result) => {
      const version = result.version ? `，服务端版本 ${result.version}` : "";
      return `PING 成功（db ${result.db}${version}）`;
    });
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
            <Drawer.Heading>{editing ? "编辑连接" : "添加 Redis 连接"}</Drawer.Heading>
            <p className="mt-2 text-sm leading-6 text-muted">
              密码加密保存在平台后端，CLI 与 AI 都拿不到明文；v1 支持 standalone 实例（含 Valkey），集群与哨兵暂不支持。
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
                    placeholder="cache-main"
                    description="CLI 用名称引用：redisctl exec <名称> -- …"
                  />
                  <Choice
                    label="写策略"
                    value={input.writePolicy}
                    onChange={(writePolicy) =>
                      setInput({ ...input, writePolicy: writePolicy as RedisConnectionInput["writePolicy"] })
                    }
                    options={Object.entries(policyLabels).map(([value, label]) => ({ value, label }))}
                    description={
                      input.writePolicy === "readonly"
                        ? "写命令一律拒绝，只允许读。"
                        : input.writePolicy === "confirm"
                          ? "读命令直接执行，写命令进入待批队列，Web 端批准后才执行。"
                          : "读写全部直接执行，仍逐条审计（高危命令仍被黑名单拦截）。"
                    }
                  />
                </div>
                <div className="grid gap-4 sm:grid-cols-[1fr_160px]">
                  <Field
                    label="主机"
                    value={input.host}
                    onChange={(host) => setInput({ ...input, host })}
                    required
                    placeholder="10.0.0.9 或 redis.example.com"
                  />
                  <NumericField
                    label="端口"
                    value={String(input.port)}
                    onChange={(port) => setInput({ ...input, port: Number(port) || 6379 })}
                    min={1}
                    max={65535}
                    required
                  />
                </div>
                <div className="grid gap-4 sm:grid-cols-[120px_1fr]">
                  <NumericField
                    label="db 编号"
                    value={String(input.db)}
                    onChange={(db) => setInput({ ...input, db: Number(db) || 0 })}
                    min={0}
                    max={15}
                    description="db 由连接固定，exec 中不能用 SELECT 切换"
                  />
                  <Field
                    label="密码（可选，无密码实例留空）"
                    type="password"
                    value={clearPassword ? "" : password}
                    onChange={setPassword}
                    autoComplete="new-password"
                    description={
                      editing && connection?.hasPassword && !clearPassword
                        ? "留空保留已保存的密码。"
                        : editing && !connection?.hasPassword
                          ? "当前无密码。"
                          : undefined
                    }
                  />
                </div>
                {editing && connection?.hasPassword && (
                  <Toggle
                    label="清除已保存的密码"
                    selected={clearPassword}
                    onChange={setClearPassword}
                    description="勾选后连接改为无密码实例（密码字段留空即可保存）。"
                  />
                )}
                <Toggle
                  label="启用 TLS"
                  selected={input.tls}
                  onChange={(tls) => setInput({ ...input, tls })}
                  description="v1 不校验服务端证书（内网自签证书场景）。"
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
                    editing && !clearPassword && !password.trim()
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

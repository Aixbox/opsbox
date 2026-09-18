import { Button, Drawer, Spinner } from "@heroui/react";
import { useId, useState } from "react";
import { Choice, Field, NumericField, TextFieldArea, Toggle } from "~/features/fields";
import { Notice, errorMessage } from "~/features/shared";
import { sshApi, type SshConnection, type SshConnectionInput } from "~/lib/api/ssh";
import { policyLabels } from "./shared";

function fresh(): SshConnectionInput {
  return {
    name: "",
    host: "",
    port: 22,
    username: "root",
    authType: "key",
    execPolicy: "audit",
    enabled: true,
    remark: "",
  };
}

function fromConnection(connection: SshConnection): SshConnectionInput {
  return {
    name: connection.name,
    host: connection.host,
    port: connection.port,
    username: connection.username,
    authType: connection.authType,
    execPolicy: connection.execPolicy,
    enabled: connection.enabled,
    remark: connection.remark,
  };
}

/** 连接新建 / 编辑抽屉。凭证不回显：编辑时留空保持原值。 */
export function ConnectionDrawer({
  connection,
  onClose,
  onSaved,
}: {
  connection?: SshConnection;
  onClose: () => void;
  onSaved: () => Promise<unknown>;
}) {
  const formId = useId();
  const [input, setInput] = useState<SshConnectionInput>(() => (connection ? fromConnection(connection) : fresh()));
  const [password, setPassword] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [resetHostKey, setResetHostKey] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const editing = Boolean(connection);
  const keepHint = editing ? "留空保留已保存的值。" : undefined;

  async function submit() {
    if (pending) return;
    setPending(true);
    setError(undefined);
    try {
      const payload: SshConnectionInput = { ...input, resetHostKey };
      if (password.trim() || !editing) payload.password = password;
      if (privateKey.trim() || !editing) payload.privateKey = privateKey;
      if (passphrase.trim() || !editing) payload.passphrase = passphrase;
      await sshApi.saveConnection(payload, connection?.id);
      await onSaved();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setPending(false);
    }
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
            <Drawer.Heading>{editing ? "编辑连接" : "添加 SSH 连接"}</Drawer.Heading>
            <p className="mt-2 text-sm leading-6 text-muted">
              凭证加密保存在平台后端，CLI 与 AI 都拿不到明文；首次连接会记录主机指纹，之后每次校验。
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
                    placeholder="prod-web-1"
                    description="CLI 用名称引用：sshctl exec <名称> -- …"
                  />
                  <Choice
                    label="执行策略"
                    value={input.execPolicy}
                    onChange={(execPolicy) =>
                      setInput({ ...input, execPolicy: execPolicy as SshConnectionInput["execPolicy"] })
                    }
                    options={Object.entries(policyLabels).map(([value, label]) => ({ value, label }))}
                    description={
                      input.execPolicy === "approve"
                        ? "每条命令 / 传输 / 终端都先进待批队列，Web 端批准后才执行。"
                        : "黑名单拦截 + 全量审计，其余直接执行；动态命令可按设置强制审批。"
                    }
                  />
                </div>
                <div className="grid gap-4 sm:grid-cols-[1fr_160px]">
                  <Field
                    label="主机"
                    value={input.host}
                    onChange={(host) => setInput({ ...input, host })}
                    required
                    placeholder="10.0.0.8 或 host.example.com"
                  />
                  <NumericField
                    label="端口"
                    value={String(input.port)}
                    onChange={(port) => setInput({ ...input, port: Number(port) || 22 })}
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
                  <Choice
                    label="认证方式"
                    value={input.authType}
                    onChange={(authType) =>
                      setInput({ ...input, authType: authType as SshConnectionInput["authType"] })
                    }
                    options={[
                      { value: "key", label: "私钥" },
                      { value: "password", label: "密码" },
                    ]}
                  />
                </div>
                {input.authType === "password" ? (
                  <Field
                    label="密码"
                    type="password"
                    value={password}
                    onChange={setPassword}
                    required={!editing || !connection?.hasPassword}
                    autoComplete="new-password"
                    description={keepHint}
                  />
                ) : (
                  <>
                    <TextFieldArea
                      label="私钥（PEM / OpenSSH 格式）"
                      value={privateKey}
                      onChange={setPrivateKey}
                      rows={7}
                      code
                      required={!editing || !connection?.hasPrivateKey}
                      description={keepHint ?? "粘贴 -----BEGIN OPENSSH PRIVATE KEY----- 开头的整段内容。"}
                    />
                    <Field
                      label="私钥口令（可选）"
                      type="password"
                      value={passphrase}
                      onChange={setPassphrase}
                      autoComplete="new-password"
                      description={keepHint}
                    />
                  </>
                )}
                <Field
                  label="备注"
                  value={input.remark}
                  onChange={(remark) => setInput({ ...input, remark })}
                  maxLength={500}
                />
                <Toggle label="启用" selected={input.enabled} onChange={(enabled) => setInput({ ...input, enabled })} />
                {editing && connection?.hostKey && (
                  <Toggle
                    label="重新记录主机指纹"
                    selected={resetHostKey}
                    onChange={setResetHostKey}
                    description={`当前指纹 ${connection.hostKey}。服务器重装后指纹会变化，勾选后下次连接重新信任。`}
                  />
                )}
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

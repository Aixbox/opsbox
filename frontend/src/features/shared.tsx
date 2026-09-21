import { Alert, AlertDialog, Button, Chip, EmptyState, Modal, Spinner, Table } from "@heroui/react";
import { Copy, Inbox, RefreshCw } from "lucide-react";
import { useEffect, useId, useState, type ReactNode } from "react";
import { TableLoadingState } from "~/features/table-loading-state";
import { TablePagination, type TablePaginationProps } from "~/features/table-pagination";

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "操作失败，请重试";
}
export function dateTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN", { hour12: false });
}

/** 挂起态的延迟显示：快于 delayMs 的操作完全不显示加载态（一闪而过的 spinner 比没有更糟），慢操作在延迟后照常出现。 */
function useDelayedFlag(active: boolean, delayMs = 300) {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    if (!active) {
      setShown(false);
      return;
    }
    const timer = setTimeout(() => setShown(true), delayMs);
    return () => clearTimeout(timer);
  }, [active, delayMs]);
  return shown;
}

const statuses: Record<string, { label: string; color: "success" | "warning" | "danger" | "default" | "accent" }> = {
  active: { label: "已启用", color: "success" },
  disabled: { label: "已停用", color: "default" },
  pending: { label: "等待处理", color: "warning" },
  error: { label: "连接异常", color: "danger" },
  running: { label: "执行中", color: "accent" },
  failed: { label: "失败", color: "danger" },
  success: { label: "成功", color: "success" },
};
export function StatusChip({ status, label }: { status: string; label?: string }) {
  const item = statuses[status];
  return (
    <Chip size="sm" variant="soft" color={item?.color ?? "default"}>
      <Chip.Label>{label ?? item?.label ?? status}</Chip.Label>
    </Chip>
  );
}
export function PageHeader({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <header className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        <p className="mt-2 max-w-2xl text-sm leading-6 text-muted">{description}</p>
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-2">{children}</div>
    </header>
  );
}
export function RefreshButton({
  onPress,
  pending,
  variant = "tertiary",
}: {
  onPress: () => void;
  pending?: boolean;
  variant?: "secondary" | "tertiary";
}) {
  return (
    <Button variant={variant} isPending={pending} onPress={onPress}>
      <RefreshCw size={16} aria-hidden="true" />
      刷新
    </Button>
  );
}

export function Notice({
  children,
  title,
  status = "default",
}: {
  children?: ReactNode;
  title: string;
  status?: "default" | "success" | "danger" | "warning";
}) {
  return (
    <Alert className="border border-separator bg-surface-secondary shadow-none" status={status}>
      <Alert.Indicator />
      <Alert.Content className="min-w-0">
        <Alert.Title>{title}</Alert.Title>
        {children && <Alert.Description className="break-words">{children}</Alert.Description>}
      </Alert.Content>
    </Alert>
  );
}

export function QueryError({ error, retry }: { error: unknown; retry?: () => void }) {
  if (!error) return null;
  return (
    <Notice status="danger" title="加载失败">
      {errorMessage(error)}
      {retry && (
        <Button size="sm" className="ml-2" variant="tertiary" onPress={retry}>
          重试
        </Button>
      )}
    </Notice>
  );
}

/**
 * 连接抽屉的「测试连接」：用表单当前值发一次真实连接探测，结果就地展示。
 * onTest 返回成功描述文案（如版本号 / uname），抛错则显示后端给的具体原因。
 * failureHint：失败时附带的背景说明（如「本次用的是已保存的凭证」）。
 * 注意不传 isPending：RAC 的 isPending 会走 live-region 公告路径，在 WebView2 里
 * 会引发弹层重绘闪烁。宽度稳定用 min-w 保证：空闲时只渲染文案（无空槽空白），
 * pending 时 spinner + 「测试中…」恰好也在 min-w 之内，按钮不跳动。
 */
export function TestConnectionButton({
  onTest,
  disabled,
  failureHint,
}: {
  onTest: () => Promise<string>;
  disabled?: boolean;
  failureHint?: string;
}) {
  const [pending, setPending] = useState(false);
  const [success, setSuccess] = useState<string>();
  const [error, setError] = useState<string>();
  // 加载态延迟出现：本地库的测试常在 300ms 内完成，直接显示会闪一下
  const busy = useDelayedFlag(pending);

  async function run() {
    if (pending) return;
    setPending(true);
    setSuccess(undefined);
    setError(undefined);
    try {
      setSuccess(await onTest());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="space-y-3">
      <Button variant="secondary" isDisabled={disabled || busy} className="min-w-28" onPress={() => void run()}>
        {busy && <Spinner size="sm" color="current" />}
        {busy ? "测试中…" : "测试连接"}
      </Button>
      {busy && (
        <Notice status="default" title="正在测试连接">
          真实拨号探测中，远程主机可能需要数秒，请稍候。
        </Notice>
      )}
      {success && (
        <Notice status="success" title="连接成功">
          {success}
        </Notice>
      )}
      {error && (
        <Notice status="danger" title="连接失败">
          {error}
          {failureHint && <span className="mt-1 block text-xs text-muted">{failureHint}</span>}
        </Notice>
      )}
    </div>
  );
}

/**
 * 列表行内的「测试」按钮：自带 pending 态（spinner + 「测试中…」并禁用），
 * 动作与结果反馈复用页面级 actions.run（成功/失败提示照旧出现在页面顶部）。
 * SSH 等远程主机的测试耗时可达数秒，没有这个状态用户会以为点击没生效。
 */
export function RowTestButton({
  run,
  action,
  successMessage,
  disabled,
}: {
  run: (action: () => Promise<unknown>, message: string) => Promise<void>;
  action: () => Promise<unknown>;
  successMessage: string;
  disabled?: boolean;
}) {
  const [pending, setPending] = useState(false);
  // 加载态延迟出现：行内测试本地服务极快，直接显示会闪烁
  const busy = useDelayedFlag(pending);
  return (
    <Button
      size="sm"
      variant="tertiary"
      isDisabled={disabled || busy}
      className="min-w-24"
      onPress={() => {
        setPending(true);
        void run(action, successMessage).finally(() => setPending(false));
      }}
    >
      {busy && <Spinner size="sm" color="current" />}
      {busy ? "测试中…" : "测试"}
    </Button>
  );
}
export interface TableColumn<T> {
  key: string;
  label: string;
  render: (row: T) => ReactNode;
}

export function DataTable<T extends { id: number }>({
  label,
  rows,
  columns,
  loading,
  empty,
  failed,
  pagination,
}: {
  label: string;
  rows: T[];
  columns: TableColumn<T>[];
  loading?: boolean;
  failed?: boolean;
  empty: string;
  pagination?: TablePaginationProps;
}) {
  return (
    <Table className="w-full min-w-0">
      <Table.ScrollContainer>
        <Table.Content aria-label={label} className="min-w-[760px]">
          <Table.Header>
            {columns.map((column, index) => (
              <Table.Column key={column.key} id={column.key} isRowHeader={index === 0}>
                {column.label}
              </Table.Column>
            ))}
          </Table.Header>
          <Table.Body
            renderEmptyState={() =>
              loading ? (
                <TableLoadingState />
              ) : (
                <EmptyState className="flex min-h-40 items-center justify-center gap-3 px-4 text-sm text-muted">
                  <Inbox size={20} aria-hidden="true" />
                  {failed ? "数据暂不可用，请重试" : empty}
                </EmptyState>
              )
            }
          >
            {rows.map((row) => (
              <Table.Row key={row.id} id={row.id}>
                {columns.map((column) => (
                  <Table.Cell key={column.key}>{column.render(row)}</Table.Cell>
                ))}
              </Table.Row>
            ))}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
      {pagination && rows.length > 0 && (
        <Table.Footer>
          <TablePagination {...pagination} />
        </Table.Footer>
      )}
    </Table>
  );
}

export function FormDialog({
  title,
  children,
  onClose,
  onSubmit,
  submitLabel = "保存",
  description,
  submitDisabled,
  dialogClassName = "w-full sm:max-w-2xl",
}: {
  title: string;
  description?: string;
  children: ReactNode;
  onClose: () => void;
  onSubmit: () => Promise<unknown>;
  submitLabel?: string;
  submitDisabled?: boolean;
  /** 覆盖弹窗宽度：单字段的小表单用 sm:max-w-md 之类，别撑成整块大表单 */
  dialogClassName?: string;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const formId = useId();
  return (
    <Modal.Backdrop
      isOpen
      isDismissable={!pending}
      isKeyboardDismissDisabled={pending}
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <Modal.Container scroll="inside">
        <Modal.Dialog className={dialogClassName}>
          <Modal.CloseTrigger isDisabled={pending} />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
            {description && <p className="mt-2 text-sm leading-6 text-muted">{description}</p>}
          </Modal.Header>
          <Modal.Body>
            <form
              id={formId}
              className="space-y-5"
              onSubmit={async (event) => {
                event.preventDefault();
                if (pending || submitDisabled) return;
                setPending(true);
                setError(undefined);
                try {
                  await onSubmit();
                  onClose();
                } catch (error) {
                  setError(errorMessage(error));
                } finally {
                  setPending(false);
                }
              }}
            >
              <fieldset disabled={pending} className="min-w-0 space-y-5">
                {children}
              </fieldset>
              {error && (
                <Notice status="danger" title="保存失败">
                  {error}
                </Notice>
              )}
            </form>
          </Modal.Body>
          <Modal.Footer>
            <Button variant="secondary" isDisabled={pending} onPress={onClose}>
              取消
            </Button>
            <Button type="submit" form={formId} isPending={pending} isDisabled={submitDisabled}>
              {pending && <Spinner size="sm" color="current" />}
              {submitLabel}
            </Button>
          </Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  );
}

export interface Confirmation {
  title: string;
  description: string;
  action: () => Promise<unknown>;
  label?: string;
  danger?: boolean;
}
export function ConfirmDialog({ confirmation, onClose }: { confirmation: Confirmation | null; onClose: () => void }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  if (!confirmation) return null;
  return (
    <AlertDialog.Backdrop
      isOpen
      isDismissable={!pending}
      isKeyboardDismissDisabled={pending}
      onOpenChange={(open) => {
        if (!open && !pending) {
          setError(undefined);
          onClose();
        }
      }}
    >
      <AlertDialog.Container>
        <AlertDialog.Dialog className="sm:max-w-md">
          <AlertDialog.Header>
            <AlertDialog.Icon status={confirmation.danger ? "danger" : "accent"} />
            <AlertDialog.Heading>{confirmation.title}</AlertDialog.Heading>
          </AlertDialog.Header>
          <AlertDialog.Body>
            <p className="text-sm leading-6">{confirmation.description}</p>
            {error && (
              <p role="alert" className="mt-3 text-sm text-danger">
                {error}
              </p>
            )}
          </AlertDialog.Body>
          <AlertDialog.Footer>
            <Button
              variant="tertiary"
              isDisabled={pending}
              onPress={() => {
                setError(undefined);
                onClose();
              }}
            >
              取消
            </Button>
            <Button
              variant={confirmation.danger ? "danger" : "primary"}
              isPending={pending}
              onPress={async () => {
                if (pending) return;
                setPending(true);
                setError(undefined);
                try {
                  await confirmation.action();
                  onClose();
                } catch (error) {
                  setError(errorMessage(error));
                } finally {
                  setPending(false);
                }
              }}
            >
              {confirmation.label ?? "确认"}
            </Button>
          </AlertDialog.Footer>
        </AlertDialog.Dialog>
      </AlertDialog.Container>
    </AlertDialog.Backdrop>
  );
}

export function DetailDialog({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
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
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          <Modal.Body className="space-y-5">{children}</Modal.Body>
          <Modal.Footer>
            <Button variant="secondary" onPress={onClose}>
              关闭
            </Button>
          </Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  );
}

/** 使用说明卡片里的「复制给 AI Agent 的提示词」：一键复制，用户粘给自己的 Agent 即可按协议操控 */
export function AgentPrompt({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    const fallback = () => {
      const area = document.createElement("textarea");
      area.value = text;
      document.body.appendChild(area);
      area.select();
      document.execCommand("copy");
      area.remove();
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).catch(fallback);
    } else {
      fallback();
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 2000);
  };
  return (
    <div className="mt-3">
      <div className="flex items-center justify-between gap-2">
        <p className="font-medium text-foreground">复制给 AI Agent 的提示词</p>
        <Button size="sm" variant="secondary" onPress={copy}>
          {copied ? (
            "已复制"
          ) : (
            <>
              <Copy size={14} aria-hidden="true" />
              复制
            </>
          )}
        </Button>
      </div>
      <pre className="mt-1.5 max-h-72 overflow-auto rounded-lg bg-default p-3 font-mono text-xs leading-5 whitespace-pre-wrap">
        {text}
      </pre>
    </div>
  );
}

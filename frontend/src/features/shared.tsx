import { Alert, AlertDialog, Button, Chip, EmptyState, Modal, Spinner, Table } from "@heroui/react";
import { Inbox, RefreshCw } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
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

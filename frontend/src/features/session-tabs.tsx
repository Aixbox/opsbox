import { Button, Tabs } from "@heroui/react";
import { Pencil, Plus, Power } from "lucide-react";
import { useState } from "react";
import { Field } from "~/features/fields";
import { FormDialog } from "~/features/shared";
import { sessionLabel, type SessionListItem } from "./use-session-list";

export type SessionTabItem = Pick<SessionListItem, "sessionId" | "seq" | "name" | "attached">;

/** 会话名称上限，与后端 console.MaxNameLength 一致 */
export const sessionNameMaxLength = 40;

/**
 * 会话标签页（SSH 终端 / Redis / SQL 控制台共用）：一个连接可以同时开多个会话，
 * 像终端软件的标签页那样切换；标签上显示会话名（没起名就是「会话 #N」）。
 * 右侧是针对当前标签的操作：重命名、结束，以及新建一个会话。
 *
 * 会话在服务端持久保留——关掉抽屉再打开还能接回来，所以「结束」是显式动作，不再随面板关闭发生。
 * 窄屏下标签条与操作按钮自动换行；标签多到放不下时 Tabs.ListContainer 会显示滚动箭头。
 *
 * 两处对 HeroUI 默认样式的覆盖，都是终端标签栏的需要：
 * - 标签按内容定宽（HeroUI 默认 w-full 平分整行：只有一个会话时标签会撑满整条栏，
 *   新建第二个后每个又缩成一半，宽度跟着数量变）；
 * - 下划线不做过渡。HeroUI 的下划线是 React Aria 的共享元素过渡：切换时把旧下划线的像素宽度
 *   写进新下划线的内联样式、下一帧再撤掉。撤销那一步在开发模式的严格模式（副作用双调用）下会被取消，
 *   旧宽度就永久留在元素上。关掉过渡后 React Aria 不再写快照，下划线总是与选中的标签同宽。
 */
export function SessionTabs({
  items,
  activeId,
  onSelect,
  onCreate,
  onTerminate,
  onRename,
  creating = false,
  disabled = false,
}: {
  items: SessionTabItem[];
  activeId: string | null;
  onSelect: (sessionId: string) => void;
  onCreate: () => void;
  onTerminate: (sessionId: string) => void;
  /** 保存名字；抛错时由弹窗展示。空串表示恢复默认 */
  onRename: (sessionId: string, name: string) => Promise<unknown>;
  creating?: boolean;
  disabled?: boolean;
}) {
  const [renaming, setRenaming] = useState<SessionTabItem | null>(null);
  const [draft, setDraft] = useState("");
  const active = items.find((item) => item.sessionId === activeId);

  function startRename() {
    if (!active) return;
    setDraft(active.name);
    setRenaming(active);
  }

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-0 flex-1">
          {active ? (
            <Tabs
              variant="secondary"
              className="w-full"
              selectedKey={active.sessionId}
              onSelectionChange={(key) => onSelect(String(key))}
            >
              <Tabs.ListContainer>
                <Tabs.List aria-label="会话">
                  {items.map((item) => (
                    <Tabs.Tab key={item.sessionId} id={item.sessionId} className="w-auto shrink-0">
                      {/* 悬停提示跟随当前名字（标签 max-w-40 会截断长名字）；附带默认编号方便对照 CLI 会话列表 */}
                      <span
                        className="max-w-40 truncate"
                        title={item.name ? `${item.name}（会话 #${item.seq}）` : undefined}
                      >
                        {sessionLabel(item)}
                      </span>
                      {item.attached > 1 && <span className="ml-1 text-xs text-muted">{item.attached} 端接入</span>}
                      <Tabs.Indicator className="transition-none" />
                    </Tabs.Tab>
                  ))}
                </Tabs.List>
              </Tabs.ListContainer>
            </Tabs>
          ) : (
            <span className="text-xs text-muted">这个连接上没有打开的会话</span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button size="sm" variant="tertiary" isDisabled={disabled || !active} onPress={startRename}>
            <Pencil size={14} aria-hidden="true" />
            重命名
          </Button>
          <Button
            size="sm"
            variant="danger"
            isDisabled={disabled || !active}
            onPress={() => {
              if (active) onTerminate(active.sessionId);
            }}
          >
            <Power size={14} aria-hidden="true" />
            结束
          </Button>
          <Button size="sm" variant="secondary" isPending={creating} isDisabled={disabled} onPress={onCreate}>
            <Plus size={14} aria-hidden="true" />
            新建
          </Button>
        </div>
      </div>
      {renaming && (
        <FormDialog
          title={`重命名 ${sessionLabel(renaming)}`}
          description="名字显示在标签页与 CLI 的会话列表里，只保存在服务端内存中，随会话一起消失。留空则恢复默认的「会话 #N」。"
          submitLabel="保存"
          dialogClassName="w-full sm:max-w-md"
          onClose={() => setRenaming(null)}
          onSubmit={() => onRename(renaming.sessionId, draft)}
        >
          <Field
            label="会话名称"
            value={draft}
            onChange={setDraft}
            placeholder={`会话 #${renaming.seq}`}
            maxLength={sessionNameMaxLength}
            autoComplete="off"
          />
        </FormDialog>
      )}
    </>
  );
}

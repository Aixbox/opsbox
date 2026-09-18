import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "~/features/shared";

/** 会话列表项的公共字段（SSH 终端 / Redis / SQL 控制台的 SessionSummary 都满足） */
export interface SessionListItem {
  sessionId: string;
  seq: number;
  /** 用户起的名字；空串表示没起过 */
  name: string;
  attached: number;
  createdAt: string;
  lastSeenAt: string;
}

/** 给人看的会话名：起过名字就用名字，否则退回默认的「会话 #N」 */
export function sessionLabel(item: Pick<SessionListItem, "seq" | "name">) {
  return item.name || `会话 #${item.seq}`;
}

/** 确认框标题里对会话的称呼：会话「部署机」 / 会话 #3 */
export function sessionMention(item: Pick<SessionListItem, "seq" | "name">) {
  return item.name ? `会话「${item.name}」` : `会话 #${item.seq}`;
}

/**
 * 空列表时是否要自动补一个新会话。
 *
 * 自动补的场景：首次打开面板（列表为空）、当前会话被闲置回收 / 在别处被结束 / 服务重启丢失
 * （列表从非空变为空）。这些情况下用户没有表达「我想要空面板」，直接给一个新会话比报错或空屏更好。
 *
 * 不自动补的场景（返回 false）：
 *  - 用户刚主动结束了会话（userTerminated）：「结束」就是要空，不越俎代庖；
 *  - 上一次新建失败了（createFailed）：对不可达的主机自动重试会形成请求风暴，界面已显示错误，让用户决定；
 *  - 正在新建中（creating）：不要重复发起。
 *
 * loaded 必须是「本次挂载已拿到新鲜列表」：重新打开面板时 react-query 会先把上次挂载的缓存
 * （gcTime 5 分钟内）连同成功状态一起回放，此时列表可能早已过时——旧会话或许已被回收、
 * 别处或许已建了新会话。基于过期缓存做决定，轻则接入一个已死的会话，重则在新鲜列表
 * 揭示已有会话之后又多建一个，出现双会话。所以必须等本轮 fetch 落地。
 */
export function shouldAutoCreate(input: {
  autoCreate: boolean;
  loaded: boolean;
  itemCount: number;
  userTerminated: boolean;
  creating: boolean;
  createFailed: boolean;
}): boolean {
  if (!input.autoCreate || !input.loaded || input.itemCount > 0) return false;
  return !input.userTerminated && !input.creating && !input.createFailed;
}

export interface SessionListOptions<T extends SessionListItem> {
  queryKey: readonly unknown[];
  list: (signal?: AbortSignal) => Promise<{ items: T[] }>;
  /** 新建会话，返回 sessionId。SSH 在 approve 策略下会在这里一直等到批准 */
  create: () => Promise<string>;
  /** 主动结束会话 */
  close: (sessionId: string) => Promise<unknown>;
  /** 给会话起名；空串恢复默认 */
  rename: (sessionId: string, name: string) => Promise<unknown>;
  /** 首次加载列表为空时自动新建一条：第一次打开面板的体验与「打开即有会话」一致 */
  autoCreate?: boolean;
}

/** 选最近活跃的会话：重新打开面板时接回上次用的那个 */
export function pickMostRecent<T extends SessionListItem>(items: T[]): T | undefined {
  return items.reduce<T | undefined>((best, item) => {
    if (!best) return item;
    return new Date(item.lastSeenAt).getTime() > new Date(best.lastSeenAt).getTime() ? item : best;
  }, undefined);
}

/**
 * 一个连接上的会话列表 + 当前选中的会话（SSH 终端 / Redis / SQL 控制台共用）。
 *
 * 会话是持久的：面板关掉只是断开接入，服务端会话还在。这个 hook 负责三件事——
 * 拉列表（定时刷新，别处结束的会话会自动消失）、维护「当前会话」（被结束后切到最近活跃的那个）、
 * 新建 / 改名 / 结束。接入（WebSocket）由各面板按 activeId 自行处理。
 */
export function useSessionList<T extends SessionListItem>(options: SessionListOptions<T>) {
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const query = useQuery({
    queryKey: options.queryKey,
    queryFn: ({ signal }) => optionsRef.current.list(signal),
    refetchInterval: 5_000,
  });
  const { refetch, isSuccess, isFetchedAfterMount } = query;
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  // 用户刚主动结束过会话：此时列表变空是用户要的，不自动补（恢复非空后解除）
  const userTerminatedRef = useRef(false);

  // 按序号升序排列，标签页的顺序稳定
  const items = useMemo(() => [...(query.data?.items ?? [])].sort((a, b) => a.seq - b.seq), [query.data]);

  // 当前会话在渲染期间同步推导：选中的那个还在列表里就用它，不在了（被结束 / 被回收 / 首次加载）
  // 就退回最近活跃的那个。如果等副作用再纠正，会话被结束后的那一帧 activeId 指向已消失的会话，
  // 标签栏会先卸载再重建，闪一下。
  const activeId = useMemo(() => {
    if (selectedId && items.some((item) => item.sessionId === selectedId)) return selectedId;
    return pickMostRecent(items)?.sessionId ?? null;
  }, [items, selectedId]);

  // 把推导出的回退结果落进状态：之后别的会话变得更活跃时，当前会话不会跟着跳
  useEffect(() => {
    if (isSuccess && activeId !== selectedId) setSelectedId(activeId);
  }, [isSuccess, activeId, selectedId]);

  const create = useCallback(async () => {
    setCreating(true);
    setCreateError(null);
    try {
      const sessionId = await optionsRef.current.create();
      await refetch();
      setSelectedId(sessionId);
      return sessionId;
    } catch (error) {
      setCreateError(errorMessage(error));
      return null;
    } finally {
      setCreating(false);
    }
  }, [refetch]);

  // 空列表自动补一个新会话。不只是首次打开：当前会话被闲置回收 / 在别处被结束 / 服务重启丢失后
  // （列表从有到无）也自动新建，不让用户面对「会话不存在」的报错或空面板。用户主动结束、
  // 上一次新建失败、正在新建时不补——见 shouldAutoCreate。
  // loaded 用 isSuccess && isFetchedAfterMount：重开面板时 react-query 先回放上次挂载的缓存，
  // 必须等本轮新鲜列表落地再决定，否则会基于过期缓存接入已死的会话、甚至多建出重复会话。
  useEffect(() => {
    if (items.length > 0) {
      userTerminatedRef.current = false;
      return;
    }
    if (
      shouldAutoCreate({
        autoCreate: optionsRef.current.autoCreate ?? false,
        loaded: isSuccess && isFetchedAfterMount,
        itemCount: items.length,
        userTerminated: userTerminatedRef.current,
        creating,
        createFailed: createError !== null,
      })
    ) {
      void create();
    }
  }, [isSuccess, isFetchedAfterMount, items.length, creating, createError, create]);

  const terminate = useCallback(
    async (sessionId: string) => {
      userTerminatedRef.current = true;
      await optionsRef.current.close(sessionId);
      await refetch();
    },
    [refetch],
  );

  const rename = useCallback(
    async (sessionId: string, name: string) => {
      await optionsRef.current.rename(sessionId, name);
      await refetch();
    },
    [refetch],
  );

  return {
    items,
    activeId,
    active: items.find((item) => item.sessionId === activeId),
    select: setSelectedId,
    create,
    creating,
    createError,
    terminate,
    rename,
    refetch,
    loaded: isSuccess && isFetchedAfterMount,
    loadError: query.error,
  };
}

import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Notice, errorMessage } from "~/features/shared";
import { redisKey } from "./shared";

/** 页面级操作反馈：执行 → 刷新 redis 查询 → 顶部提示（与 ssh / messaging 模块同构） */
export function useRedisActions() {
  const client = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<{ error: boolean; text: string }>();
  const refresh = () => client.invalidateQueries({ queryKey: redisKey });
  async function run(action: () => Promise<unknown>, message: string) {
    if (busy) return;
    setBusy(true);
    try {
      await action();
      await refresh();
      setFeedback({ error: false, text: message });
    } catch (error) {
      setFeedback({ error: true, text: errorMessage(error) });
    } finally {
      setBusy(false);
    }
  }
  return {
    busy,
    run,
    refresh,
    feedback: feedback && <Notice status={feedback.error ? "danger" : "success"} title={feedback.text} />,
  };
}

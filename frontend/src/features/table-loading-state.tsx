import { Skeleton } from "@heroui/react";

export function TableLoadingState({ label = "正在加载数据" }: { label?: string }) {
  return (
    <div aria-label={label} className="flex min-h-40 w-full flex-col justify-center gap-3 px-4 py-5" role="status">
      {Array.from({ length: 4 }, (_, index) => (
        <div key={index} className="flex items-center gap-3">
          <Skeleton className="h-3 w-1/4 rounded" />
          <Skeleton className="h-3 flex-1 rounded" />
          <Skeleton className="h-3 w-1/5 rounded" />
        </div>
      ))}
    </div>
  );
}

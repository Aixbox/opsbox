/** Go 后端共享类型与查询串工具 */

/** 统一分页响应（items/total/page/pageSize） */
export interface PageData<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

/** 序列化查询参数；undefined/null/空串跳过 */
export function buildQuery(params: object): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params as Record<string, unknown>)) {
    if (value !== undefined && value !== null && value !== "") search.set(key, String(value));
  }
  return search.toString();
}

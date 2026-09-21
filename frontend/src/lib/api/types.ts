/** Go 后端共享类型与查询串工具 */

/** 序列化查询参数；undefined/null/空串跳过。返回串不含前导 `?`，拼接时需自行补（`path?${buildQuery(...)}`）。 */
export function buildQuery(params: object): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params as Record<string, unknown>)) {
    if (value !== undefined && value !== null && value !== "") search.set(key, String(value));
  }
  return search.toString();
}

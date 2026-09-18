import { ListBox, Pagination, Select, Spinner } from "@heroui/react";

const defaultPageSizeOptions = [10, 20, 50, 100];

export type TablePaginationProps = {
  page: number;
  pageSize: number;
  total: number;
  onChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  pageSizeOptions?: number[];
  pending?: boolean;
};

export function TablePagination({
  page,
  pageSize,
  total,
  onChange,
  onPageSizeChange,
  pageSizeOptions = defaultPageSizeOptions,
  pending,
}: TablePaginationProps) {
  const last = Math.max(1, Math.ceil(total / pageSize));

  return (
    <Pagination size="sm">
      <Pagination.Summary className="flex flex-wrap items-center gap-3">
        <span>
          共 {total} 条 · 第 {page} / {last} 页
        </span>
        {pending && <Spinner size="sm" />}
        <Select
          aria-label="每页显示条数"
          className="w-28"
          isDisabled={pending}
          value={String(pageSize)}
          variant="secondary"
          onChange={(value) => {
            const nextPageSize = Number(value);
            if (Number.isFinite(nextPageSize) && nextPageSize > 0) onPageSizeChange(nextPageSize);
          }}
        >
          <Select.Trigger>
            <Select.Value />
            <Select.Indicator />
          </Select.Trigger>
          <Select.Popover>
            <ListBox>
              {pageSizeOptions.map((size) => (
                <ListBox.Item key={size} id={String(size)} textValue={`${size} 条/页`}>
                  {size} 条/页
                  <ListBox.ItemIndicator />
                </ListBox.Item>
              ))}
            </ListBox>
          </Select.Popover>
        </Select>
      </Pagination.Summary>
      <Pagination.Content>
        <Pagination.Item>
          <Pagination.Previous isDisabled={page <= 1 || pending} onPress={() => onChange(Math.max(1, page - 1))}>
            <Pagination.PreviousIcon />
            <span>上一页</span>
          </Pagination.Previous>
        </Pagination.Item>
        {Array.from({ length: last }, (_, index) => index + 1).map((pageNumber) => (
          <Pagination.Item key={pageNumber}>
            <Pagination.Link isActive={pageNumber === page} onPress={() => onChange(pageNumber)}>
              {pageNumber}
            </Pagination.Link>
          </Pagination.Item>
        ))}
        <Pagination.Item>
          <Pagination.Next isDisabled={page >= last || pending} onPress={() => onChange(Math.min(last, page + 1))}>
            <span>下一页</span>
            <Pagination.NextIcon />
          </Pagination.Next>
        </Pagination.Item>
      </Pagination.Content>
    </Pagination>
  );
}

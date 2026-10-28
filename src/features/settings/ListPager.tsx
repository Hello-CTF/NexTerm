// Shared client-side pager for settings lists that can grow (devices, login sessions, shares).
import { useState } from "react";

export function usePagedList<T>(items: readonly T[], pageSize = 8) {
  const [page, setPage] = useState(0);
  const pageCount = Math.max(1, Math.ceil(items.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  return {
    pageItems: items.slice(currentPage * pageSize, (currentPage + 1) * pageSize),
    currentPage,
    pageCount,
    paged: items.length > pageSize,
    setPage,
  };
}

export function ListPager({ currentPage, pageCount, setPage }: { currentPage: number; pageCount: number; setPage: (page: number) => void }) {
  return (
    <div className="mt-1 flex items-center gap-2 px-2.5 py-1 text-[11px] text-neutral-500">
      <span>
        第 {currentPage + 1} / {pageCount} 页
      </span>
      <div className="nx-spacer" />
      <button className="nx-btn nx-btn-ghost nx-btn-xs" disabled={currentPage === 0} onClick={() => setPage(currentPage - 1)}>
        上一页
      </button>
      <button className="nx-btn nx-btn-ghost nx-btn-xs" disabled={currentPage >= pageCount - 1} onClick={() => setPage(currentPage + 1)}>
        下一页
      </button>
    </div>
  );
}

import { useEffect, useState, type ReactNode } from "react";
import { t } from "../../i18n";

export const LIST_PAGE_SIZE = 50;

// usePaging slices a list into pages. The page resets to the first whenever resetKey changes (a new search or filter)
// and is clamped when a refresh shrinks the list.
export function usePaging<T>(items: readonly T[], resetKey: string, pageSize = LIST_PAGE_SIZE) {
  const [page, setPage] = useState(0);
  const [key, setKey] = useState(resetKey);
  if (key !== resetKey) {
    setKey(resetKey);
    setPage(0);
  }
  const pages = Math.max(1, Math.ceil(items.length / pageSize));
  const current = Math.min(page, pages - 1);
  return {
    page: current,
    pages,
    shown: items.slice(current * pageSize, (current + 1) * pageSize),
    setPage,
    // pageOf is the page that holds the item at index.
    pageOf: (index: number) => Math.floor(index / pageSize),
  };
}

// ListToolbar is the row above a host list: the search box, the filters, and the count of what is shown.
export function ListToolbar({
  label,
  placeholder,
  query,
  onQuery,
  total,
  matched,
  unit,
  children,
}: {
  label: string;
  placeholder: string;
  query: string;
  onQuery: (query: string) => void;
  total: number;
  matched: number;
  unit: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <input
        className="nx-input min-w-0 flex-1 basis-48"
        type="search"
        aria-label={label}
        placeholder={placeholder}
        value={query}
        onChange={(e) => onQuery(e.target.value)}
      />
      {children}
      <span className="nx-hint whitespace-nowrap text-[11.5px]" data-list-count="">
        {matched === total ? t("共 {total} {unit}", { total, unit }) : t("匹配 {matched} / {total} {unit}", { matched, total, unit })}
      </span>
    </div>
  );
}

// Pager moves between the pages of a host list; it renders nothing for a single page.
export function Pager({ label, page, pages, onPage }: { label: string; page: number; pages: number; onPage: (page: number) => void }) {
  if (pages <= 1) return null;
  return (
    <div className="flex items-center justify-end gap-1.5 text-[11.5px]" role="group" aria-label={label}>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={page === 0} onClick={() => onPage(page - 1)}>
        {t("上一页")}
      </button>
      <span className="nx-hint" aria-live="polite">
        {t("第")} {page + 1} / {pages} {t("页")}
      </span>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={page >= pages - 1} onClick={() => onPage(page + 1)}>
        {t("下一页")}
      </button>
    </div>
  );
}

// ListFocus asks a host list to show one item: nonce changes on every request, so asking twice for the same item
// still scrolls to it.
export interface ListFocus<T> {
  value: T;
  nonce: number;
}

// useListFocus brings a requested item into view: it clears the search and filters when they hide the item, turns
// to its page, highlights it and scrolls it into view. indexOf finds the item among the rows shown now; it returns -1
// when the item is filtered out or not loaded.
export function useListFocus<T>({
  focus,
  indexOf,
  filtered,
  clearFilters,
  setPage,
  pageOf,
  rowSelector,
}: {
  focus: ListFocus<T> | null | undefined;
  indexOf: (value: T) => number;
  filtered: boolean;
  clearFilters: () => void;
  setPage: (page: number) => void;
  pageOf: (index: number) => number;
  rowSelector: (value: T) => string;
}): T | null {
  const [highlight, setHighlight] = useState<T | null>(null);
  const [pending, setPending] = useState<ListFocus<T> | null>(null);
  const [handled, setHandled] = useState<number | null>(null);
  if (focus && focus.nonce !== handled) {
    setHandled(focus.nonce);
    setPending(focus);
  }
  const index = pending ? indexOf(pending.value) : -1;
  useEffect(() => {
    if (!pending) return;
    if (index < 0) {
      // Hidden by the search or a filter: show everything, then look again. Not loaded at all: keep waiting for data.
      if (filtered) clearFilters();
      return;
    }
    setPage(pageOf(index));
    setHighlight(pending.value);
    setPending(null);
  }, [pending, index, filtered, clearFilters, setPage, pageOf]);
  useEffect(() => {
    if (highlight === null) return;
    const row = document.querySelector<HTMLElement>(rowSelector(highlight));
    row?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  }, [highlight, rowSelector]);
  return highlight;
}

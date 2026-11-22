import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { EditorState } from "@codemirror/state";
import { runScopeHandlers, type EditorView, type Panel } from "@codemirror/view";
import {
  SearchQuery,
  closeSearchPanel,
  findNext,
  findPrevious,
  getSearchQuery,
  replaceAll,
  replaceNext,
  setSearchQuery,
} from "@codemirror/search";
import { IconArrowDown, IconArrowUp, IconChevronDown, IconChevronRight, IconClose, IconSearch } from "../../ui/icons";
import { isImeEnter } from "../../ui/imeEnter";
import { t } from "../../i18n";

export const SEARCH_COUNT_LIMIT = 1000;

type Flag = "caseSensitive" | "wholeWord" | "regexp";

export interface SearchMatchCount {
  total: number;
  // Position of the selected match among the counted ones, or -1 when the selection is not a match.
  index: number;
  capped: boolean;
}

export function countSearchMatches(state: EditorState, query: SearchQuery): SearchMatchCount | null {
  if (!query.valid) return null;
  const { from, to } = state.selection.main;
  const cursor = query.getCursor(state);
  let total = 0;
  let index = -1;
  for (let step = cursor.next(); !step.done; step = cursor.next()) {
    if (step.value.from === from && step.value.to === to) index = total;
    total += 1;
    if (total >= SEARCH_COUNT_LIMIT) return { total, index, capped: true };
  }
  return { total, index, capped: false };
}

function countLabel(count: SearchMatchCount): string {
  if (count.total === 0) return t("无结果");
  const total = count.capped ? `${SEARCH_COUNT_LIMIT}+` : String(count.total);
  if (count.index < 0) return t("{count} 个匹配", { count: total });
  return `${count.index + 1}/${total}`;
}

export function EditorSearchBar({ view, state }: { view: EditorView; state: EditorState }) {
  const query = getSearchQuery(state);
  const [search, setSearch] = useState(query.search);
  const [replace, setReplace] = useState(query.replace);
  const [replaceOpen, setReplaceOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);

  // The query also changes from outside the panel, as when Mod-f seeds it with the selection.
  useEffect(() => setSearch(query.search), [query.search]);
  useEffect(() => setReplace(query.replace), [query.replace]);
  // CodeMirror focuses the field itself only when the panel is already open, so the first render does it.
  useEffect(() => {
    searchRef.current?.focus();
    searchRef.current?.select();
  }, []);

  const count = useMemo(() => countSearchMatches(state, query), [state, query]);
  const regexError = query.regexp && query.search !== "" && !query.valid;

  const commit = (patch: Partial<{ search: string; replace: string } & Record<Flag, boolean>>) => {
    view.dispatch({
      effects: setSearchQuery.of(
        new SearchQuery({
          search: query.search,
          replace: query.replace,
          caseSensitive: query.caseSensitive,
          wholeWord: query.wholeWord,
          regexp: query.regexp,
          ...patch,
        }),
      ),
    });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (isImeEnter(e)) return;
    if (runScopeHandlers(view, e.nativeEvent, "search-panel")) {
      e.preventDefault();
      return;
    }
    if (e.key !== "Enter") return;
    if (e.target === searchRef.current) {
      e.preventDefault();
      (e.shiftKey ? findPrevious : findNext)(view);
    } else if (e.target instanceof HTMLInputElement && e.target.name === "replace") {
      e.preventDefault();
      (e.metaKey || e.ctrlKey ? replaceAll : replaceNext)(view);
    }
  };

  const flagButtons = (
    [
      ["caseSensitive", "Aa", t("区分大小写")],
      ["wholeWord", "ab", t("全字匹配")],
      ["regexp", ".*", t("正则表达式")],
    ] as const
  ).map(([flag, glyph, label]) => (
    <button
      key={flag}
      type="button"
      className={`nx-icon-btn nx-icon-btn-sm font-mono text-[11px] ${query[flag] ? "is-active" : ""}`}
      aria-label={label}
      aria-pressed={query[flag]}
      title={label}
      onClick={() => commit({ [flag]: !query[flag] })}
    >
      {flag === "wholeWord" ? <span className="underline underline-offset-2">{glyph}</span> : glyph}
    </button>
  ));

  return (
    <div
      className="flex flex-col gap-1 border-b border-neutral-800/60 bg-neutral-900/70 px-1.5 py-1.5"
      role="search"
      onKeyDown={onKeyDown}
    >
      <div className="flex min-w-0 items-center gap-1">
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          aria-label={t("切换替换")}
          aria-expanded={replaceOpen}
          title={t("切换替换")}
          onClick={() => setReplaceOpen((open) => !open)}
        >
          {replaceOpen ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
        </button>
        <div className="nx-field min-w-0 max-w-[280px] flex-1">
          <span className="nx-field-icon">
            <IconSearch size={12} />
          </span>
          <input
            ref={searchRef}
            className="nx-input nx-input-sm"
            name="search"
            placeholder={t("查找…")}
            aria-label={t("查找…")}
            aria-invalid={regexError ? true : undefined}
            spellCheck={false}
            autoComplete="off"
            main-field="true"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              commit({ search: e.target.value });
            }}
          />
        </div>
        <div className="flex shrink-0 items-center gap-0.5">{flagButtons}</div>
        {query.search && (
          <span
            className={`min-w-0 shrink truncate whitespace-nowrap px-1 text-[11px] tabular-nums ${
              regexError || count?.total === 0 ? "text-red-300" : "text-neutral-400"
            }`}
            role="status"
            aria-live="polite"
            data-testid="editor-search-status"
          >
            {regexError ? t("正则表达式无效") : count ? countLabel(count) : ""}
          </span>
        )}
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          aria-label={t("上一个匹配")}
          title={t("上一个")}
          onClick={() => findPrevious(view)}
        >
          <IconArrowUp size={13} />
        </button>
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          aria-label={t("下一个匹配")}
          title={t("下一个")}
          onClick={() => findNext(view)}
        >
          <IconArrowDown size={13} />
        </button>
        <div className="nx-spacer" />
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          aria-label={t("关闭搜索")}
          title={t("关闭搜索")}
          onClick={() => closeSearchPanel(view)}
        >
          <IconClose size={12} />
        </button>
      </div>
      {replaceOpen && (
        <div className="flex min-w-0 flex-wrap items-center gap-1 pl-[26px]">
          <div className="nx-field min-w-0 max-w-[280px] flex-1 basis-[160px]">
            <input
              className="nx-input nx-input-sm"
              style={{ paddingLeft: 8 }}
              name="replace"
              placeholder={t("替换为…")}
              aria-label={t("替换为…")}
              spellCheck={false}
              autoComplete="off"
              autoFocus
              value={replace}
              readOnly={state.readOnly}
              onChange={(e) => {
                setReplace(e.target.value);
                commit({ replace: e.target.value });
              }}
            />
          </div>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={state.readOnly || !query.valid}
            onClick={() => replaceNext(view)}
          >
            {t("替换")}
          </button>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={state.readOnly || !query.valid}
            onClick={() => replaceAll(view)}
          >
            {t("全部替换")}
          </button>
        </div>
      )}
    </div>
  );
}

export interface EditorSearchHost {
  dom: HTMLElement;
  view: EditorView;
  state: EditorState;
}

// editorSearchPanel replaces CodeMirror's stock find panel with an empty host that the editor fills with
// EditorSearchBar through a portal; onHost reports the host and every state it should render, and null on close.
export function editorSearchPanel(onHost: (host: EditorSearchHost | null) => void): (view: EditorView) => Panel {
  return (view) => {
    const dom = document.createElement("div");
    dom.className = "nx-cm-search";
    onHost({ dom, view, state: view.state });
    return {
      dom,
      top: true,
      update(update) {
        if (update.docChanged || update.selectionSet || update.transactions.some((tr) => tr.effects.length > 0)) {
          onHost({ dom, view, state: update.state });
        }
      },
      destroy() {
        onHost(null);
      },
    };
  };
}

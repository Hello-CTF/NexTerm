import { useEffect, useId, useState, type KeyboardEvent, type RefObject } from "react";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { IconSearch } from "../../ui/icons";
import type { SettingsSearchResult } from "./settingsIndex";

interface Props {
  query: string;
  onQueryChange: (query: string) => void;
  results: readonly SettingsSearchResult[];
  onSelect: (result: SettingsSearchResult) => void;
  inputRef: RefObject<HTMLInputElement | null>;
}

// Search box plus result list for the settings navigation. The list is a
// listbox driven from the input (aria-activedescendant), so arrow keys, Enter
// and Esc work without moving focus.
export function SettingsSearch({ query, onQueryChange, results, onSelect, inputRef }: Props) {
  const listId = useId();
  const [active, setActive] = useState(0);
  const searching = query.trim() !== "";
  useEffect(() => setActive(0), [query]);

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (isImeKeyEvent(e)) return;
    if (e.key === "ArrowDown" && results.length > 0) {
      e.preventDefault();
      setActive((i) => (i + 1) % results.length);
    } else if (e.key === "ArrowUp" && results.length > 0) {
      e.preventDefault();
      setActive((i) => (i - 1 + results.length) % results.length);
    } else if (e.key === "Enter" && results[active]) {
      e.preventDefault();
      onSelect(results[active]);
    } else if (e.key === "Escape") {
      if (query !== "") {
        e.preventDefault();
        e.stopPropagation();
        onQueryChange("");
      } else {
        e.currentTarget.blur();
      }
    }
  };

  return (
    <div className="px-3 pt-2 sm:px-5 md:px-2" data-testid="settings-search">
      <div className="relative">
        <IconSearch size={12} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-neutral-500" />
        <input
          ref={inputRef}
          type="search"
          role="combobox"
          className="nx-input nx-input-sm w-full pl-7"
          placeholder="搜索设置（按 / 聚焦）"
          aria-label="搜索设置"
          aria-expanded={searching && results.length > 0}
          aria-autocomplete="list"
          aria-controls={searching ? listId : undefined}
          aria-activedescendant={searching && results[active] ? `${listId}-${active}` : undefined}
          autoComplete="off"
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          onKeyDown={onKeyDown}
        />
      </div>
      <span className="nx-sr-only" role="status">
        {searching ? `${results.length} 个结果` : ""}
      </span>
      {searching &&
        (results.length === 0 ? (
          <p className="nx-hint px-1 py-3">
            没有找到与「{query.trim()}」相关的设置
          </p>
        ) : (
          <ul
            id={listId}
            role="listbox"
            aria-label="设置搜索结果"
            className="mt-1.5 flex max-h-64 flex-col gap-0.5 overflow-y-auto md:max-h-none"
          >
            {results.map((result, index) => (
              <li
                key={result.key}
                id={`${listId}-${index}`}
                role="option"
                aria-selected={index === active}
                className={`cursor-pointer rounded-md px-2 py-1 text-left ${
                  index === active ? "bg-neutral-800/70 text-neutral-100" : "text-neutral-300 hover:bg-neutral-800/45"
                }`}
                onMouseEnter={() => setActive(index)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onSelect(result)}
              >
                <div className="text-[12.5px]">{result.label}</div>
                <div className="nx-hint">
                  {result.kind === "item" ? result.detail : `分区 · ${result.detail}`}
                </div>
              </li>
            ))}
          </ul>
        ))}
    </div>
  );
}

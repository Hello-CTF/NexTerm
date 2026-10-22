import { useState } from "react";
import { IconSearch } from "../../ui/icons";

export interface TargetSetPickerTarget {
  id: string;
  label: string;
  sessionName: string;
  endpoint: string | null;
  tags: string[];
  workspaceNames: string[];
  envCount: number;
}

export function filterTargetSet(
  targets: readonly TargetSetPickerTarget[],
  query: string,
): TargetSetPickerTarget[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return [...targets];
  return targets.filter((target) => {
    const searchable = [
      target.label,
      target.sessionName,
      target.endpoint ?? "",
      ...target.tags,
      ...target.workspaceNames,
    ].join("\n").toLowerCase();
    return terms.every((term) => searchable.includes(term));
  });
}

export function TargetSetPicker({
  targets,
  selectedIds,
  onChange,
  disabled = false,
  maxTargets,
  metadataError,
  legend = "目标会话",
  emptyMessage = "没有已连接的 SSH 会话。请先手动连接目标，批量执行不会自动连接。",
  searchAriaLabel = "搜索批量执行目标",
}: {
  targets: readonly TargetSetPickerTarget[];
  selectedIds: readonly string[];
  onChange: (selectedIds: string[]) => void;
  disabled?: boolean;
  maxTargets: number;
  metadataError?: string | null;
  legend?: string;
  emptyMessage?: string;
  searchAriaLabel?: string;
}) {
  const [query, setQuery] = useState("");
  const filtered = filterTargetSet(targets, query);
  const selected = new Set(selectedIds);

  const toggle = (id: string) => {
    onChange(selected.has(id) ? selectedIds.filter((selectedId) => selectedId !== id) : [...selectedIds, id]);
  };

  const selectFiltered = () => {
    const next = new Set(selectedIds);
    for (const target of filtered) next.add(target.id);
    onChange([...next]);
  };

  return (
    <fieldset className="min-w-0" disabled={disabled}>
      <legend className="mb-1.5 text-[11px] text-neutral-500">{legend}（已选 {selectedIds.length}）</legend>
      {targets.length === 0 ? (
        <div className="rounded border border-dashed border-neutral-800 px-3 py-3 text-[11px] text-neutral-500">
          {emptyMessage}
        </div>
      ) : (
        <>
          <div className="nx-field mb-2 max-w-md">
            <span className="nx-field-icon">
              <IconSearch size={12} />
            </span>
            <input
              className="nx-input nx-input-sm"
              value={query}
              placeholder="搜索名称 / 主机 / 标签"
              aria-label={searchAriaLabel}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          {metadataError && (
            <div className="mb-2 text-[11px] text-amber-300" role="status">
              {metadataError}
            </div>
          )}
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <button type="button" className="nx-link" disabled={filtered.length === 0} onClick={selectFiltered}>
              全选筛选结果
            </button>
            <button type="button" className="nx-link" disabled={selectedIds.length === 0} onClick={() => onChange([])}>
              清空
            </button>
            <span className="nx-hint">
              显示 {filtered.length} / {targets.length} · 每批最多 {maxTargets} 个
            </span>
          </div>
          <div className="grid max-h-56 grid-cols-1 gap-1 overflow-y-auto rounded border border-neutral-800/70 p-2 xl:grid-cols-2">
            {filtered.map((target) => (
              <label
                key={target.id}
                className="flex min-w-0 cursor-pointer items-start gap-2 rounded px-1.5 py-1.5 text-[11px] text-neutral-300 hover:bg-neutral-800/50"
                title={target.id}
                data-target-id={target.id}
              >
                <input
                  type="checkbox"
                  className="mt-0.5 shrink-0 accent-blue-500"
                  value={target.id}
                  aria-label={`选择目标 ${target.label}`}
                  checked={selected.has(target.id)}
                  onChange={() => toggle(target.id)}
                />
                <span className="min-w-0 flex-1">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate font-medium text-neutral-200" title={target.label}>
                      {target.label}
                    </span>
                    {target.envCount > 0 && (
                      <span className="nx-badge nx-badge-amber shrink-0" title={`${target.envCount} 个环境变量`}>
                        env {target.envCount}
                      </span>
                    )}
                  </span>
                  {target.sessionName !== target.label && (
                    <span className="mt-0.5 block truncate text-[10px] text-neutral-500">
                      会话：{target.sessionName}
                    </span>
                  )}
                  {target.endpoint && (
                    <span className="mt-0.5 block truncate font-mono text-[10px] text-neutral-400" title={target.endpoint}>
                      {target.endpoint}
                    </span>
                  )}
                  <span className="mt-0.5 block truncate text-[10px] text-neutral-500">
                    工作区：{target.workspaceNames.length > 0 ? target.workspaceNames.join("、") : "未关联"}
                  </span>
                  {target.tags.length > 0 && (
                    <span className="mt-0.5 block truncate text-[10px] text-neutral-500">
                      标签：{target.tags.join("、")}
                    </span>
                  )}
                </span>
              </label>
            ))}
            {filtered.length === 0 && (
              <div className="col-span-full px-2 py-3 text-[11px] text-neutral-500">
                没有匹配当前搜索的已连接 SSH 会话。
              </div>
            )}
          </div>
        </>
      )}
    </fieldset>
  );
}

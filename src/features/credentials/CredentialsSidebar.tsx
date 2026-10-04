import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { openCredentialsTab, openCredentialsViewTab, useCredentialsTabId } from "../../app/store";
import { NewCredentialModal } from "./NewCredentialModal";
import { KIND_META, KIND_ORDER, kindMeta } from "./meta";
import { useRefreshCredentials, useVaultUnlock } from "./useVaultUnlock";
import { workspaceViewport } from "../terminal/workspaceLayout";
import { describeError } from "../../ui/errorText";
import { IconCode, IconLock, IconPlus, IconRefresh, IconSearch } from "../../ui/icons";

function closeOverlayDockAfterNav() {
  const coarse = window.matchMedia?.("(pointer: coarse)").matches ?? false;
  if (workspaceViewport(window.innerWidth, coarse).overlaySidebars) {
    useUi.getState().setLeftOpen(false);
  }
}

export function CredentialsSidebar() {
  const { leftOpen, leftWidth } = useUi();
  const refresh = useRefreshCredentials();
  const unlock = useVaultUnlock();

  const [search, setSearch] = useState("");
  const [kindFilter, setKindFilter] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const status = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const credentials = useQuery({
    queryKey: ["credentials"],
    queryFn: () => vaultApi.listCredentials(),
    refetchOnWindowFocus: false,
  });
  const selectedId = useCredentialsTabId();

  if (!leftOpen) return null;

  const st = status.data;
  const locked = !!st?.initialized && !st.unlocked;
  const protectionOn = st?.mode === "master";
  const all = credentials.data ?? [];

  const q = search.trim().toLowerCase();
  const list = all.filter((c) => {
    if (kindFilter && c.kind !== kindFilter) return false;
    if (!q) return true;
    return c.name.toLowerCase().includes(q) || kindMeta(c.kind).label.toLowerCase().includes(q);
  });

  const counts: Record<string, number> = {};
  for (const c of all) counts[c.kind] = (counts[c.kind] ?? 0) + 1;

  const unused = all.filter((c) => c.usedBy.length === 0).length;

  return (
    <div
      className="flex h-full shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950"
      style={{ width: leftWidth }}
    >
      <div className="flex h-[34px] shrink-0 items-center gap-1.5 px-2.5">
        <span className="text-xs font-semibold tracking-wide text-neutral-200">凭据</span>
        <span className="nx-count">{all.length}</span>
        <div className="nx-spacer" />
        {protectionOn && !locked && (
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="立即锁定（锁定后需输入密码才能使用凭据）"
            onClick={() =>
              void vaultApi
                .lock()
                .then(refresh)
                .catch(() => undefined)
            }
          >
            <IconLock size={14} />
          </button>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="新建凭据"
          onClick={() => setCreating(true)}
        >
          <IconPlus size={14} />
        </button>
      </div>

      {locked && (
        <div className="mx-2 mb-2 flex shrink-0 items-center gap-1.5 rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-1.5 text-[11.5px] text-amber-300/90">
          <IconLock size={12} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">已锁定 · 解锁后可查看</span>
          <button className="nx-btn nx-btn-xs nx-btn-outline" onClick={() => void unlock()}>
            解锁
          </button>
        </div>
      )}

      <div className="px-2 pb-2">
        <div className="nx-field">
          <span className="nx-field-icon">
            <IconSearch size={13} />
          </span>
          <input
            className="nx-input nx-input-sm"
            placeholder="搜索凭据"
            aria-label="搜索凭据"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {all.length > 0 && (
        <div className="flex shrink-0 flex-wrap gap-1 px-2.5 pb-2">
          <FilterChip
            active={kindFilter === null}
            label="全部"
            count={all.length}
            onClick={() => setKindFilter(null)}
          />
          {KIND_ORDER.filter((k) => counts[k]).map((k) => (
            <FilterChip
              key={k}
              active={kindFilter === k}
              label={KIND_META[k].label}
              count={counts[k]}
              onClick={() => setKindFilter((cur) => (cur === k ? null : k))}
            />
          ))}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {credentials.isLoading ? (
          <div className="nx-hint px-2 py-6 text-center">加载中…</div>
        ) : credentials.isError && !credentials.data ? (
          <div className="px-2 py-6 text-center">
            <div className="nx-hint">
              凭据列表加载失败
              <br />
              {describeError(credentials.error)}
            </div>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm mt-2"
              onClick={() => void credentials.refetch()}
            >
              <IconRefresh size={12} />
              重试
            </button>
          </div>
        ) : list.length === 0 ? (
          <div className="nx-hint px-2 py-8 text-center">
            {all.length === 0 ? (
              <>
                暂无凭据
                <br />
                新建资产时填的密码会自动存进来
              </>
            ) : (
              "没有匹配的凭据"
            )}
          </div>
        ) : (
          list.map((c) => {
            const meta = kindMeta(c.kind);
            const sel = c.id === selectedId;
            return (
              <button
                key={c.id}
                className={`nx-row w-full text-left ${sel ? "is-selected" : ""}`}
                title={`${c.name} · ${meta.label}`}
                onClick={() => {
                  openCredentialsTab(c.id);
                  closeOverlayDockAfterNav();
                }}
              >
                <span
                  className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-md ${meta.bg} ${meta.tone}`}
                >
                  <meta.Icon size={14} />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[12.5px] text-neutral-200">{c.name}</span>
                  <span className="block truncate text-[11px] text-neutral-500">
                    {meta.label}
                    {" · "}
                    {c.usedBy.length > 0 ? (
                      `被 ${c.usedBy.length} 个资产使用`
                    ) : (
                      <span className="text-[var(--nx-fg-warning)]">未使用</span>
                    )}
                  </span>
                </span>
              </button>
            );
          })
        )}
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-2.5 py-2">
        {all.length > 0 && (
          <div className="mb-2 flex items-center gap-2 text-[11px] text-neutral-500">
            <span className="whitespace-nowrap">{all.length} 条凭据</span>
            {unused > 0 && (
              <span className="whitespace-nowrap text-[var(--nx-fg-warning)]">{unused} 条未被使用</span>
            )}
            <div className="nx-spacer" />
            <span className="truncate">{protectionOn ? (locked ? "已锁定" : "已解锁") : "未启用密码保护"}</span>
          </div>
        )}
        <div className="flex items-center gap-1.5">
          <IconCode size={13} className="shrink-0 text-neutral-500" />
          <span className="min-w-0 flex-1 truncate text-[12px] text-neutral-400">凭据视图</span>
          <div className="nx-segment shrink-0">
            <button
              className="nx-segment-item"
              title="以 ssh config 风格文本查看"
              onClick={() => {
                openCredentialsViewTab("text");
                closeOverlayDockAfterNav();
              }}
            >
              文本
            </button>
            <button
              className="nx-segment-item"
              title="以 JSON 查看"
              onClick={() => {
                openCredentialsViewTab("json");
                closeOverlayDockAfterNav();
              }}
            >
              JSON
            </button>
          </div>
        </div>
      </div>

      {creating && (
        <NewCredentialModal
          onClose={() => setCreating(false)}
          onSaved={(id) => {
            setCreating(false);
            refresh();
            openCredentialsTab(id);
          }}
        />
      )}
    </div>
  );
}

function FilterChip({
  active,
  label,
  count,
  onClick,
}: {
  active: boolean;
  label: string;
  count: number;
  onClick: () => void;
}) {
  return (
    <button
      className={`flex shrink-0 items-center gap-1 rounded-full px-2 py-[3px] text-[11px] ${
        active
          ? "bg-blue-500/15 text-blue-300"
          : "text-neutral-500 hover:bg-white/[.05] hover:text-neutral-300"
      }`}
      onClick={onClick}
    >
      {label}
      <span className={active ? "text-blue-400/70" : "text-neutral-600"}>{count}</span>
    </button>
  );
}

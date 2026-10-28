import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { openCredentialsTab, useCredentialsTabId } from "../../app/store";
import { isWidthOverlayViewport } from "../../app/shell";
import { NewCredentialModal } from "./NewCredentialModal";
import { KIND_META, KIND_ORDER, kindMeta } from "./meta";
import { useRefreshCredentials, useVaultUnlock } from "./useVaultUnlock";
import { useVaultInitGate } from "./useVaultInitGate";
import { LoadError } from "../../ui/LoadError";
import { IconLock, IconPlus, IconSearch } from "../../ui/icons";

function closeOverlayDockAfterNav() {
  if (isWidthOverlayViewport()) {
    useUi.getState().setLeftOpen(false);
  }
}

export function CredentialsSidebar() {
  const leftOpen = useUi((s) => s.leftOpen);
  const leftWidth = useUi((s) => s.leftWidth);
  const refresh = useRefreshCredentials();
  const unlock = useVaultUnlock();
  const { ensureVaultInit, vaultInitGate } = useVaultInitGate();

  const [search, setSearch] = useState("");
  const [kindFilter, setKindFilter] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const pageSize = 30;
  const [creating, setCreating] = useState(false);
  const pushToast = useUi((s) => s.pushToast);

  const startCreate = () =>
    void ensureVaultInit().then((ok) => {
      if (ok) setCreating(true);
    });

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
  const protectionOn = st?.mode === "master" && !st.passwordless;
  const all = credentials.data ?? [];

  const q = search.trim().toLowerCase();
  const counts: Record<string, number> = {};
  for (const c of all) counts[c.kind] = (counts[c.kind] ?? 0) + 1;
  const activeKind = kindFilter && counts[kindFilter] ? kindFilter : null;
  const list = all.filter((c) => {
    if (activeKind && c.kind !== activeKind) return false;
    if (!q) return true;
    return c.name.toLowerCase().includes(q) || kindMeta(c.kind).label.toLowerCase().includes(q);
  });
  const pageCount = Math.max(1, Math.ceil(list.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const pageList = list.slice(currentPage * pageSize, (currentPage + 1) * pageSize);


  // A kind filter whose last credential was removed would show an empty list with no active chip.
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
            title="立即锁定凭据库（之后需输入保护密码才能使用凭据）"
            aria-label="锁定凭据库"
            onClick={() =>
              void vaultApi
                .lock()
                .then(refresh)
                .catch(() => pushToast("error", "无法锁定凭据库。请重试。"))
            }
          >
            <IconLock size={14} />
          </button>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="新建凭据"
          aria-label="新建凭据"
          onClick={startCreate}
        >
          <IconPlus size={14} />
        </button>
      </div>

      {locked && (
        <div className="mx-2 mb-2 flex shrink-0 items-center gap-1.5 rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-1.5 text-[11.5px] text-amber-300">
          <IconLock size={12} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">凭据库已锁定，解锁后才能查看</span>
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
            onChange={(e) => {
              setSearch(e.target.value);
              setPage(0);
            }}
          />
        </div>
      </div>

      {all.length > 0 && (
        <div className="flex shrink-0 flex-wrap gap-1 px-2.5 pb-2">
          <FilterChip
            active={activeKind === null}
            label="全部"
            count={all.length}
            onClick={() => {
              setKindFilter(null);
              setPage(0);
            }}
          />
          {KIND_ORDER.filter((k) => counts[k]).map((k) => (
            <FilterChip
              key={k}
              active={activeKind === k}
              label={KIND_META[k].label}
              count={counts[k]}
              onClick={() => {
                setKindFilter((cur) => (cur === k ? null : k));
                setPage(0);
              }}
            />
          ))}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {credentials.isLoading ? (
          <div className="nx-hint px-2 py-6 text-center" role="status">
            正在加载…
          </div>
        ) : credentials.isError && !credentials.data ? (
          <LoadError title="未能加载凭据列表" error={credentials.error} onRetry={() => void credentials.refetch()} />
        ) : list.length === 0 ? (
          <div className="nx-hint px-2 py-8 text-center">
            {all.length === 0 ? (
              <>
                暂无任何凭据
                <br />
                新建资产时填写的密码会自动保存到凭据库，
                也可以直接新建。
                <br />
                <button type="button" className="nx-btn nx-btn-outline nx-btn-sm mt-3" onClick={startCreate}>
                  <IconPlus size={12} />
                  新建凭据
                </button>
              </>
            ) : (
              "没有匹配的凭据"
            )}
          </div>
        ) : (
          pageList.map((c) => {
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
                      `${c.usedBy.length} 个资产在用`
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
            {unused > 0 && (
              <span className="whitespace-nowrap text-[var(--nx-fg-warning)]">{unused} 条未被资产使用</span>
            )}
            <div className="nx-spacer" />
            <span className="truncate">
              {protectionOn
                ? locked
                  ? "已锁定"
                  : "已解锁"
                : st?.mode === "not_init"
                  ? "凭据库尚未设置"
                  : st?.mode === "master"
                    ? "已启用，未设保护密码"
                    : "未启用密码保护"}
            </span>
          </div>
        )}
        {list.length > pageSize && (
          <div className="flex items-center gap-2 text-[11px] text-neutral-500">
            <span>
              第 {currentPage + 1} / {pageCount} 页
            </span>
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs"
              disabled={currentPage === 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
            >
              上一页
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs"
              disabled={currentPage >= pageCount - 1}
              onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
            >
              下一页
            </button>
          </div>
        )}
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
      {vaultInitGate}
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
      aria-pressed={active}
      className={`flex shrink-0 items-center gap-1 rounded-full px-2 py-[3px] text-[11px] ${
        active
          ? "bg-[color-mix(in_srgb,var(--color-accent)_16%,transparent)] text-blue-300"
          : "text-neutral-500 hover:bg-neutral-800/70 hover:text-neutral-300"
      }`}
      onClick={onClick}
    >
      {label}
      <span className={active ? "text-blue-300" : "text-neutral-500"}>{count}</span>
    </button>
  );
}

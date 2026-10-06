// 凭据侧边栏：左栏的第三种形态（资产 / 文件树 / 凭据）。
//
// 为什么给凭据一个独立形态：凭据会随使用自然增长（资产表单、数据库连接都会往里写），
// 原来那行"资产树底部的小按钮"既发现不了、也承不下后续要加的东西。
//
// 分工（与主区详情页）：**左栏负责"找"与"切"，详情负责"看"与"改"**。
// 左栏窄（默认 248px），塞不下值、引用关系、时间这些信息，硬塞就是两头都难用。
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { openCredentialsTab, openCredentialsViewTab, useCredentialsTabId } from "../../app/store";
import { NewCredentialModal } from "./NewCredentialModal";
import { KIND_META, KIND_ORDER, kindMeta } from "./meta";
import { useRefreshCredentials, useVaultUnlock } from "./useVaultUnlock";
import { WEB } from "../../demo";
import { IconCode, IconLock, IconPlus, IconSearch } from "../../ui/icons";

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
  // 主区详情标签当前选中的是哪条 —— 左栏高亮跟着它走，两边只有一个真相。
  const selectedId = useCredentialsTabId();

  if (!leftOpen) return null;

  const st = status.data;
  const locked = !!st?.initialized && !st.unlocked;
  const protectionOn = st?.mode === "master";
  /**
   * 能否「立即锁定」。
   *
   * ⛔ 浏览器版（服务端）**必须排除**：那里的库虽然也是 `master` 模式，但密码是
   * 部署方（懒猫 `stable_secret` / 自建时 `--master-key`）注入的随机值，**用户根本
   * 不知道**。允许锁定 = 制造死锁 —— 前端会弹一个用户答不出来的密码框，只能靠
   * 重启服务端才恢复。桌面版才该有这个入口：密码是用户自己在设置页设的。
   */
  const canLock = protectionOn && !WEB;
  const all = credentials.data ?? [];

  const q = search.trim().toLowerCase();
  const list = all.filter((c) => {
    if (kindFilter && c.kind !== kindFilter) return false;
    if (!q) return true;
    return c.name.toLowerCase().includes(q) || kindMeta(c.kind).label.toLowerCase().includes(q);
  });

  /** 各类型的条数（过滤条上显示；为 0 的类型不出现，避免一排空按钮）。 */
  const counts: Record<string, number> = {};
  for (const c of all) counts[c.kind] = (counts[c.kind] ?? 0) + 1;

  const unused = all.filter((c) => c.usedBy.length === 0).length;

  return (
    <div
      className="flex h-full shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950"
      style={{ width: leftWidth }}
    >
      {/* 头部：标题 + 计数 + 锁定 / 新建 */}
      <div className="flex h-[34px] shrink-0 items-center gap-1.5 px-2.5">
        <span className="text-xs font-semibold tracking-wide text-neutral-200">凭据</span>
        <span className="nx-count">{all.length}</span>
        <div className="nx-spacer" />
        {canLock && !locked && (
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

      {/* 锁定态横幅：列表照常可见（名称/类型/引用关系），值一律不可见 */}
      {locked && (
        <div className="mx-2 mb-2 flex shrink-0 items-center gap-1.5 rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-1.5 text-[11.5px] text-amber-300/90">
          <IconLock size={12} className="shrink-0" />
          {WEB ? (
            // 服务端：密码由部署方托管，用户无从输入 ⇒ 不给「解锁」按钮，
            // 只说明恢复方式（重启后 bootstrap 会用部署密钥自动解锁）。
            <span className="min-w-0 flex-1">已锁定 · 重启服务端后自动恢复</span>
          ) : (
            <>
              <span className="min-w-0 flex-1 truncate">已锁定 · 解锁后可查看</span>
              <button className="nx-btn nx-btn-xs nx-btn-outline" onClick={() => void unlock()}>
                解锁
              </button>
            </>
          )}
        </div>
      )}

      {/* 搜索 */}
      <div className="px-2 pb-2">
        <div className="nx-field">
          <span className="nx-field-icon">
            <IconSearch size={13} />
          </span>
          <input
            className="nx-input nx-input-sm"
            placeholder="搜索凭据"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* 类型过滤：一排计数胶囊。凭据一多，"只看私钥"比搜索更常用。
          用 wrap 而不是横向滚动 —— 248px 装不下 5 个类型，横滚会把最后一种藏起来。 */}
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

      {/* 列表 */}
      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {credentials.isLoading ? (
          <div className="nx-hint px-2 py-6 text-center">加载中…</div>
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
                onClick={() => openCredentialsTab(c.id)}
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
                      <span className="text-amber-500/90">未使用</span>
                    )}
                  </span>
                </span>
              </button>
            );
          })
        )}
      </div>

      {/* 概览 + 视图入口 */}
      <div className="shrink-0 border-t border-neutral-800/60 px-2.5 py-2">
        {all.length > 0 && (
          <div className="mb-2 flex items-center gap-2 text-[11px] text-neutral-500">
            <span>{all.length} 条凭据</span>
            {unused > 0 && <span className="text-amber-500/80">{unused} 条未被使用</span>}
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
              onClick={() => openCredentialsViewTab("text")}
            >
              文本
            </button>
            <button
              className="nx-segment-item"
              title="以 JSON 查看"
              onClick={() => openCredentialsViewTab("json")}
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

/** 类型过滤胶囊。 */
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

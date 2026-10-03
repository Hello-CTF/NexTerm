// 命令片段面板（M59）：片段的增删改查 + 插入到当前终端。
//
// 安全红线（别改坏）：
// · 选中 / 编辑片段只碰字符串，**绝不执行内容** —— 内容进入终端的唯一路径是
//   显式的「插入」按钮。
// · 插入按风险分级（见 insertRisk）：只有真正可打印的内容才只是「打字」
//   （写入 ≠ 执行，回车才运行）；含回车/换行（PTY 里 \r 就是回车，提交即执行）
//   或任何控制字符（Tab 补全 / Ctrl-C / Ctrl-D / ESC 序列等）的片段必须先
//   显式确认，不允许静默跑。
// · 确认 ≠ 删改：任何片段都按原字节写入，分级只决定要不要先问。
// · 保存 ≠ 删改：新建 / 编辑按原字节入库，trim 只做「是否空白」校验。
// · 插入目标是**当前工作区的当前终端标签**；没有就明确提示，不偷偷开新终端。
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi, terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  IconCommand,
  IconEdit,
  IconLoader,
  IconPlay,
  IconPlus,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

/** 片段行（与 facade 的 snippet_list 返回一致）。 */
export interface Snippet {
  id: string;
  name: string;
  body: string;
  groupId: string | null;
  sort: number;
}

/** 找当前工作区 → 当前面板 → 当前标签里的终端内核 tabId；没有返回 null。 */
function activeTerminalTabId(): string | null {
  const st = useUi.getState();
  const ws =
    st.workspaces.find((w) => w.id === st.activeWorkspaceId) ??
    st.workspaces[st.workspaces.length - 1] ??
    null;
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tab = pane?.tabs.find((t) => t.id === pane.activeTabId) ?? null;
  if (!tab || tab.kind !== "terminal" || !tab.tabId) return null;
  return tab.tabId;
}

/**
 * 插入风险分级：
 * - `"execute"`：含 `\r` 或 `\n` —— 规范模式下两者都提交当前输入行
 *   （PTY 里裸 `\r` 就是回车），写入即逐行执行，必须显式确认；
 * - `"control"`：含任何非可打印字符（全部 C0、DEL、C1）—— 不会替你执行
 *   命令行，但 Tab 会触发 programmable completion（补全钩子是 shell 代码，
 *   不需要回车就会运行），DEL/其余 C0/C1 可能中断前台进程、结束输入或
 *   经 ESC 序列 / readline 绑定改变终端状态，必须显式确认；
 * - `"none"`：仅真正可打印字符（0x20–0x7e 与 ≥0xa0 的 Unicode）——
 *   只是「打字」，写入不执行。
 */
type InsertRisk = "none" | "execute" | "control";

function insertRisk(body: string): InsertRisk {
  // 第一遍：回车/换行优先 —— 规范模式下 \r 与 \n 都提交当前输入行，写入即执行
  for (const ch of body) {
    const c = ch.codePointAt(0) ?? 0;
    if (c === 0x0a || c === 0x0d) return "execute";
  }
  // 第二遍：可打印白名单之外的统统要确认（含 Tab 补全与 DEL），不再设例外
  for (const ch of body) {
    const c = ch.codePointAt(0) ?? 0;
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f)) return "control";
  }
  return "none";
}

export function SnippetsPanel({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const [editing, setEditing] = useState<Snippet | "new" | null>(null);
  /** 正在删除的片段 id（行内 busy，防连点）。 */
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const snippets = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    refetchOnWindowFocus: false,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["snippets"] });

  const insert = async (s: Snippet) => {
    const tabId = activeTerminalTabId();
    if (!tabId) {
      pushToast("info", "请先在当前工作区打开一个终端，再插入片段");
      return;
    }
    const risk = insertRisk(s.body);
    if (risk === "execute") {
      // 文案如实说明执行风险：回车/换行在 PTY 里就是「替你按回车」。
      const n = s.body.match(/[\r\n]/g)?.length ?? 0;
      const ok = await ask(
        `片段「${s.name}」包含 ${n} 处回车/换行：插入时每一处都会立即提交执行（相当于替你按回车）。\n仍要插入吗？`,
        { kind: "warning" },
      );
      if (!ok) return;
    } else if (risk === "control") {
      const ok = await ask(
        `片段「${s.name}」包含终端控制字符（Tab 补全 / DEL / Ctrl-C / Ctrl-D / ESC 序列等）：\n插入不会替你执行命令行，但 Tab 补全钩子本身就是 shell 代码（不需要回车就会运行），其他控制字符也可能触发 readline/终端绑定动作、中断前台进程或改变终端状态。\n仍要插入吗？`,
        { kind: "warning" },
      );
      if (!ok) return;
    }
    try {
      // body 永远按原字节写入（确认 ≠ 删改）；risk=none 时不含任何提交/控制字符，
      // 只是「打字」，落到终端输入行，由用户检查后再决定回车。
      await terminalApi.write(tabId, new TextEncoder().encode(s.body));
      pushToast(
        "success",
        risk === "none"
          ? `已插入「${s.name}」 · 未执行，确认后回车运行`
          : risk === "execute"
            ? `已插入「${s.name}」 · 已按原样写入，其中回车/换行处已逐行执行`
            : `已插入「${s.name}」 · 已按原样写入，控制字符可能已改变终端状态`,
      );
      onClose();
    } catch (e) {
      pushToast("error", `插入失败：${describeError(e)}`);
    }
  };

  const remove = async (s: Snippet) => {
    if (deletingId) return;
    const ok = await ask(`删除片段「${s.name}」？此操作不可恢复。`, { kind: "warning" });
    if (!ok) return;
    setDeletingId(s.id);
    try {
      await assetApi.snippetDelete(s.id);
      pushToast("info", "已删除");
      refresh();
    } catch (e) {
      pushToast("error", `删除失败：${describeError(e)}`);
    } finally {
      setDeletingId(null);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[420px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">命令片段</span>
          <div className="nx-spacer" />
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="新建片段"
            onClick={() => setEditing("new")}
          >
            <IconPlus size={14} />
          </button>
        </div>
        <div className="nx-modal-body">
          {snippets.isPending && (
            <div className="flex items-center gap-2 px-1 py-6 text-[12px] text-neutral-500">
              <IconLoader size={13} className="animate-spin" /> 正在加载片段…
            </div>
          )}
          {snippets.isError && (
            <div className="px-1 py-5 text-center">
              <div className="flex items-center justify-center gap-1.5 text-[12px] text-red-400">
                <IconXCircle size={13} /> 加载失败:{describeError(snippets.error)}
              </div>
              <button className="nx-btn nx-btn-outline nx-btn-sm mt-3" onClick={() => void snippets.refetch()}>
                重试
              </button>
            </div>
          )}
          {snippets.data && snippets.data.length === 0 && (
            <div className="nx-hint px-1 py-8 text-center">
              还没有片段 — 点右上角 + 新建一个。插入只把命令写进终端，含回车/换行或控制字符时会先确认。
            </div>
          )}
          {snippets.data?.map((s) => (
            <div key={s.id} className="nx-row group mb-0.5">
              <IconCommand size={13} className="shrink-0 text-neutral-500" />
              <span className="min-w-0 flex-1 truncate text-[12.5px]">{s.name}</span>
              <span className="nx-row-actions">
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="插入到当前终端"
                  disabled={deletingId === s.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    void insert(s);
                  }}
                >
                  <IconPlay size={12} />
                </button>
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="编辑片段"
                  disabled={deletingId === s.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    setEditing(s);
                  }}
                >
                  <IconEdit size={12} />
                </button>
                <button
                  className="nx-icon-btn nx-icon-btn-sm is-danger"
                  title="删除片段"
                  disabled={deletingId === s.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    void remove(s);
                  }}
                >
                  {deletingId === s.id ? (
                    <IconLoader size={12} className="animate-spin" />
                  ) : (
                    <IconTrash size={12} />
                  )}
                </button>
              </span>
            </div>
          ))}
        </div>
        <div className="nx-modal-footer">
          <span className="nx-hint mr-auto">插入 = 写入终端输入行；回车/换行或控制字符会先确认</span>
          <button className="nx-btn nx-btn-ghost" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
      {editing && (
        <SnippetEditor
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            refresh();
          }}
        />
      )}
    </div>
  );
}

/** 新建 / 编辑片段。保存失败时错误留在弹窗里，可直接重试。 */
function SnippetEditor({
  initial,
  onClose,
  onSaved,
}: {
  initial: Snippet | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [name, setName] = useState(initial?.name ?? "");
  const [body, setBody] = useState(initial?.body ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    if (saving) return;
    const n = name.trim();
    // trim 只用于空白校验：正文按原字节保存（首尾空格 / Tab / CR / LF 都可能有语义，
    // 尾部 CR/LF 正是「确认后提交执行」的内容），与插入路径一样不改字节。
    if (!n || !body.trim()) {
      setError("名称和内容都不能为空");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (initial) {
        await assetApi.snippetUpdate(initial.id, n, body);
      } else {
        await assetApi.snippetCreate(n, body);
      }
      pushToast("success", initial ? "已保存" : "已创建");
      onSaved();
    } catch (e) {
      // 错误留在弹窗里（附在保存按钮上方），改完直接再点保存即重试。
      setError(describeError(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[420px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">
            {initial ? "编辑片段" : "新建片段"}
          </span>
        </div>
        <div className="nx-modal-body">
          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="看容器状态"
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label">内容</label>
            <textarea
              className="nx-textarea font-mono text-[11.5px]"
              rows={4}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="docker ps --format '{{.Names}}'"
            />
            <div className="nx-hint mt-1.5">
              ↳ 插入终端时只写入输入行，不会自动执行；含回车/换行或控制字符的片段插入前会再确认一次。
            </div>
          </div>
          {error && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {error}
            </div>
          )}
        </div>
        <div className="nx-modal-footer">
          <button className="nx-btn nx-btn-ghost" onClick={onClose} disabled={saving}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={saving || !name.trim() || !body.trim()}
            onClick={() => void save()}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </div>
    </div>
  );
}

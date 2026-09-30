// 资产同步：桌面 ↔ 盒子上的服务端。
//
// # 为什么落在设置页而不是新开一个视图
//
// `App.tsx` 的视图是 switch 分支 + 标签栏，新增一档要动布局与导航；而同步是
// 「配一次、偶尔用」的功能，塞进设置页够用，又不牵动主界面。
//
// # 两种运行模式在界面上是两份东西，刻意不合并
//
// - **桌面**是发起方：配盒子地址与访问令牌，然后勾资产、选方向推 / 拉；
// - **浏览器版**（也就是盒子上的服务端）是被同步的那一端：只把自己的令牌
//   交出去（复制到桌面去填）。
//
// 硬合成一张卡片会两头都不像 —— 一边要填密码框，另一边要显示一串可复制的码。
//
// # 方向是显式的，「冲突」由人判断
//
// 每行一个勾选框，方向由**按钮**决定：勾中的资产按「推送到盒子」或「从盒子拉取」
// 走。没有自动合并：SSH 私钥这类载荷根本不可合并（不是文本），所以冲突（两边都
// 改过）只在行上标出来，让人自己选 —— 这正是「同步哪些资产可选」的自然延伸。
import { useCallback, useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { syncApi } from "../../ipc/commands";
import type { DigestEntry, ImportReport, SyncDigest, SyncLink } from "../../ipc/types";
import { useUi } from "../../app/store";
import { DEMO, WEB } from "../../demo";
import { describeError } from "../../ui/errorText";
import {
  IconCheckCircle,
  IconCopy,
  IconDownload,
  IconInfo,
  IconRefresh,
  IconServer,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

/** 一行 = 一个资产在两边的状态。 */
type RowState = "local-only" | "remote-only" | "both" | "same";

interface Row {
  id: string;
  name: string;
  kind: string;
  host: string | null;
  local?: DigestEntry;
  remote?: DigestEntry;
  state: RowState;
  localNewer: boolean;
  /** 任一侧有墓碑（软删除）—— 这类行要显眼标出来。 */
  deleted: boolean;
}

function mergeRows(local: SyncDigest | null, remote: SyncDigest | null): Row[] {
  const map = new Map<string, Row>();
  for (const e of local?.assets ?? []) {
    map.set(e.id, {
      id: e.id,
      name: e.name,
      kind: e.kind,
      host: e.host,
      local: e,
      state: "local-only",
      localNewer: false,
      deleted: e.deletedAt !== null,
    });
  }
  for (const e of remote?.assets ?? []) {
    const cur = map.get(e.id);
    if (!cur) {
      map.set(e.id, {
        id: e.id,
        name: e.name,
        kind: e.kind,
        host: e.host,
        remote: e,
        state: "remote-only",
        localNewer: false,
        deleted: e.deletedAt !== null,
      });
      continue;
    }
    cur.remote = e;
    cur.deleted = cur.deleted || e.deletedAt !== null;
    cur.localNewer = (cur.local?.updatedAt ?? 0) > e.updatedAt;
    cur.state = cur.local?.updatedAt === e.updatedAt ? "same" : "both";
  }
  return [...map.values()].sort((a, b) => {
    // 有差异的排前面：一致的行没什么可操作的，沉底不碍事
    const rank = (r: Row) => (r.state === "same" ? 1 : 0);
    return rank(a) - rank(b) || a.name.localeCompare(b.name);
  });
}

function stateLabel(r: Row): { text: string; tone: string; hint: string } {
  if (r.deleted) {
    return {
      text: r.local ? "本机已删" : "盒子已删",
      tone: "nx-badge-amber",
      hint: "这条在某一侧已被删除。往另一侧同步会把它一并删掉。",
    };
  }
  switch (r.state) {
    case "local-only":
      return { text: "仅本机", tone: "", hint: "盒子上还没有这条。推送会新建。" };
    case "remote-only":
      return { text: "仅盒子", tone: "", hint: "本机还没有这条。拉取会在本机新建。" };
    case "same":
      return { text: "已一致", tone: "nx-badge-green", hint: "两边内容相同。" };
    default:
      return r.localNewer
        ? { text: "本机较新", tone: "nx-badge-amber", hint: "两边都改过，本机这份更新。" }
        : { text: "盒子较新", tone: "nx-badge-amber", hint: "两边都改过，盒子那份更新。" };
  }
}

export function SyncCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();
  /** 浏览器版 = 盒子上的服务端 = 被同步的那一端。 */
  const isServer = WEB;

  const [link, setLink] = useState<SyncLink | null>(null);
  const [draft, setDraft] = useState({ url: "", tokenKind: "platform", token: "", insecure: false });
  const [showToken, setShowToken] = useState(false);
  const [ownToken, setOwnToken] = useState<string | null>(null);

  const [local, setLocal] = useState<SyncDigest | null>(null);
  const [remote, setRemote] = useState<SyncDigest | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [withCreds, setWithCreds] = useState(false);
  const [force, setForce] = useState(false);

  const [busy, setBusy] = useState<null | "test" | "push" | "pull">(null);
  const [report, setReport] = useState<{ dir: "push" | "pull"; data: ImportReport } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const rows = useMemo(() => mergeRows(local, remote), [local, remote]);

  const refreshLocal = useCallback(
    () =>
      syncApi
        .digest()
        .then(setLocal)
        .catch(() => undefined),
    [],
  );

  useEffect(() => {
    if (DEMO) return;
    void refreshLocal();
    if (isServer) {
      void syncApi
        .token()
        .then(setOwnToken)
        .catch(() => undefined);
      return;
    }
    void syncApi
      .linkGet()
      .then((l) => {
        setLink(l);
        setDraft({ url: l.url, tokenKind: l.tokenKind || "platform", token: "", insecure: l.insecure });
      })
      .catch(() => undefined);
  }, [isServer, refreshLocal]);

  if (DEMO) return null;

  /** 保存连接配置（令牌留空 = 不改动已存的），然后立刻验一次。 */
  const saveAndTest = async () => {
    setBusy("test");
    setError(null);
    try {
      const saved = await syncApi.linkSet({
        url: draft.url,
        tokenKind: draft.tokenKind,
        // 留空表示「不改令牌」——所以只在非空时才传这个字段
        ...(draft.token.trim() ? { token: draft.token.trim() } : {}),
        insecure: draft.insecure,
      });
      setLink(saved);
      setDraft((d) => ({ ...d, token: "" }));
      const d = await syncApi.remoteDigest();
      setRemote(d);
      pushToast("success", `已连接盒子（对端标识 ${d.origin}，${d.assets.length} 条资产）`);
    } catch (e) {
      setError(describeError(e));
      setRemote(null);
    } finally {
      setBusy(null);
      void syncApi
        .linkGet()
        .then(setLink)
        .catch(() => undefined);
    }
  };

  const reloadRemote = async () => {
    setBusy("test");
    setError(null);
    try {
      setRemote(await syncApi.remoteDigest());
    } catch (e) {
      setError(describeError(e));
      setRemote(null);
    } finally {
      setBusy(null);
    }
  };

  const transfer = async (dir: "push" | "pull") => {
    const ids = [...selected];
    if (ids.length === 0) {
      pushToast("error", "先勾选要同步的资产");
      return;
    }
    setBusy(dir);
    setError(null);
    setReport(null);
    try {
      const data =
        dir === "push"
          ? await syncApi.push(ids, withCreds, force)
          : await syncApi.pull(ids, withCreds, force);
      setReport({ dir, data });
      setSelected(new Set());
      // 本地库被写过了：资产树 / 凭据页都要跟着刷新
      void qc.invalidateQueries();
      await refreshLocal();
      if (remote) await reloadRemote();
      const moved = data.assetsCreated + data.assetsUpdated;
      pushToast(
        moved > 0 ? "success" : "error",
        moved > 0
          ? `同步完成：新建 ${data.assetsCreated} · 更新 ${data.assetsUpdated}`
          : "没有任何条目被写入，请看下方原因",
      );
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const selectAll = (on: boolean) =>
    setSelected(on ? new Set(rows.filter((r) => r.state !== "same").map((r) => r.id)) : new Set());

  const connected = !!link?.verifiedAt && !link.lastError;

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">资产同步</span>
        {isServer ? (
          <span className="nx-badge">这台是同步目标</span>
        ) : connected ? (
          <span className="nx-badge nx-badge-green">已连接</span>
        ) : (
          <span className="nx-badge">未连接</span>
        )}
      </div>

      {isServer ? <ServerTokenBody token={ownToken} onRotate={setOwnToken} /> : null}

      {!isServer && (
        <>
          <p className="nx-hint mb-3.5">
            把本机配好的服务器资产送到微服的 NexTerm（或取回来），之后在手机、浏览器上
            就能直接连。方向由你按按钮决定，同步哪些资产由你勾选。
          </p>

          <div className="flex flex-col gap-2">
            <label className="flex items-center gap-2">
              <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">盒子地址</span>
              <input
                className="nx-input min-w-0 flex-1 font-mono"
                placeholder="https://nexterm.heiyu.space"
                value={draft.url}
                onChange={(e) => setDraft((d) => ({ ...d, url: e.target.value }))}
              />
            </label>

            <label className="flex items-center gap-2">
              <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">令牌类型</span>
              <select
                className="nx-input w-[180px]"
                value={draft.tokenKind}
                onChange={(e) => setDraft((d) => ({ ...d, tokenKind: e.target.value }))}
              >
                <option value="platform">平台 API 令牌</option>
                <option value="app">应用同步令牌</option>
              </select>
              <span className="nx-hint">
                {draft.tokenKind === "platform"
                  ? "在盒子上执行 hc api_auth_token gen 生成"
                  : "在盒子版「设置 → 资产同步」里查看"}
              </span>
            </label>

            <label className="flex items-center gap-2">
              <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">访问令牌</span>
              <input
                type={showToken ? "text" : "password"}
                className="nx-input min-w-0 flex-1 font-mono"
                autoComplete="off"
                placeholder={link?.token ? "留空 = 不修改已保存的令牌" : "粘贴令牌"}
                value={draft.token}
                onChange={(e) => setDraft((d) => ({ ...d, token: e.target.value }))}
              />
              <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setShowToken((v) => !v)}>
                {showToken ? "隐藏" : "显示"}
              </button>
            </label>

            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                checked={draft.insecure}
                onChange={(e) => setDraft((d) => ({ ...d, insecure: e.target.checked }))}
              />
              <span className="text-[12px] text-neutral-300">
                跳过证书校验
                <span className="nx-hint block">
                  懒猫盒子用微服自己签发的证书，不勾这一项会握手失败。仅在你确认地址没错时勾选。
                </span>
              </span>
            </label>
          </div>

          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={busy !== null || !draft.url.trim()}
              onClick={() => void saveAndTest()}
            >
              {busy === "test" ? <IconRefresh size={12} className="animate-spin" /> : <IconCheckCircle size={12} />}
              {busy === "test" ? "连接中…" : "保存并测试连接"}
            </button>
            {link?.url && (
              <button
                className="nx-btn nx-btn-outline nx-btn-sm"
                disabled={busy !== null}
                onClick={() => void reloadRemote()}
              >
                <IconRefresh size={12} />
                重新读取盒子
              </button>
            )}
            {link?.lastError && (
              <span className="nx-hint text-amber-300">上次失败：{link.lastError}</span>
            )}
          </div>

          {error && (
            <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{error}</span>
            </div>
          )}

          {remote && <CompareTable
            rows={rows}
            selected={selected}
            onToggle={toggle}
            onSelectAll={selectAll}
            withCreds={withCreds}
            onWithCreds={setWithCreds}
            force={force}
            onForce={setForce}
            busy={busy}
            onTransfer={transfer}
            localOrigin={local?.origin ?? ""}
            remoteOrigin={remote.origin}
          />}

          {report && <ReportBody dir={report.dir} data={report.data} />}
        </>
      )}
    </section>
  );
}

/** 盒子端（浏览器版）：把自己的令牌交出去。 */
function ServerTokenBody({
  token,
  onRotate,
}: {
  token: string | null;
  onRotate: (t: string | null) => void;
}) {
  const { pushToast } = useUi();
  const [revealed, setRevealed] = useState(false);

  return (
    <>
      <p className="nx-hint mb-3.5">
        桌面版的 NexTerm 可以连到这台微服，把上面配好的服务器资产推过来（或取回去）。
        把下面的令牌填到桌面版「设置 → 资产同步 → 访问令牌」里即可。
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <code className="nx-code min-w-0 flex-1 truncate font-mono text-[12px]">
          {token ? (revealed ? token : "•".repeat(32)) : "（读取中…）"}
        </code>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setRevealed((v) => !v)}>
          {revealed ? "隐藏" : "显示"}
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={!token}
          onClick={() => {
            if (!token) return;
            void navigator.clipboard
              ?.writeText(token)
              .then(() => pushToast("success", "令牌已复制"))
              .catch(() => pushToast("error", "复制失败，请手动选中复制"));
          }}
        >
          <IconCopy size={12} />
          复制
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          onClick={() => {
            void syncApi
              .rotateToken()
              .then((t) => {
                onRotate(t);
                setRevealed(true);
                pushToast("success", "已换新令牌，旧的立刻失效");
              })
              .catch((e) => pushToast("error", describeError(e)));
          }}
        >
          重置令牌
        </button>
      </div>

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          <b>这个令牌等同于对这台微服的完全控制权</b>，不只是同步资产 —— 它连的是
          同一套命令接口。别贴到聊天、截图或工单里；怀疑泄露了就点「重置令牌」。
        </div>
      </div>
    </>
  );
}

/** 本地 / 盒子 资产对照表 + 方向按钮。 */
function CompareTable(props: {
  rows: Row[];
  selected: Set<string>;
  onToggle: (id: string) => void;
  onSelectAll: (on: boolean) => void;
  withCreds: boolean;
  onWithCreds: (v: boolean) => void;
  force: boolean;
  onForce: (v: boolean) => void;
  busy: null | "test" | "push" | "pull";
  onTransfer: (dir: "push" | "pull") => void;
  localOrigin: string;
  remoteOrigin: string;
}) {
  const {
    rows,
    selected,
    onToggle,
    onSelectAll,
    withCreds,
    onWithCreds,
    force,
    onForce,
    busy,
    onTransfer,
    localOrigin,
    remoteOrigin,
  } = props;

  const pushable = rows.filter((r) => r.state !== "remote-only").length;
  const pullable = rows.filter((r) => r.state !== "local-only").length;

  return (
    <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
      <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1.5">
        <span className="text-[12.5px] text-neutral-200">
          资产对照
          <span className="nx-hint ml-2">
            本机 · {localOrigin || "—"} ↔ 盒子 · {remoteOrigin}
          </span>
        </span>
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => onSelectAll(true)}>
          全选有差异的
        </button>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => onSelectAll(false)}>
          清空
        </button>
      </div>

      {rows.length === 0 ? (
        <div className="nx-hint py-3 text-[12px]">两边都还没有可同步的资产。</div>
      ) : (
        <div className="max-h-[320px] overflow-y-auto rounded border border-neutral-800/60">
          {rows.map((r) => {
            const st = stateLabel(r);
            const on = selected.has(r.id);
            return (
              <label
                key={r.id}
                className="flex cursor-pointer items-center gap-2 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0 hover:bg-neutral-800/30"
              >
                <input
                  type="checkbox"
                  className="h-3.5 w-3.5 shrink-0"
                  checked={on}
                  onChange={() => onToggle(r.id)}
                />
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={r.name}>
                  {r.name}
                </span>
                <span className="shrink-0 font-mono text-[11px] text-neutral-500">
                  {r.host ?? r.kind}
                </span>
                {r.local?.hasCred || r.remote?.hasCred ? (
                  <span className="nx-hint shrink-0" title="带凭据（密码/私钥）">
                    带密码
                  </span>
                ) : null}
                <span className={`nx-badge shrink-0 ${st.tone}`} title={st.hint}>
                  {st.text}
                </span>
              </label>
            );
          })}
        </div>
      )}

      <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
          <input
            type="checkbox"
            className="h-3.5 w-3.5"
            checked={withCreds}
            onChange={(e) => onWithCreds(e.target.checked)}
          />
          含凭据（密码 / 私钥）
        </label>
        <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
          <input
            type="checkbox"
            className="h-3.5 w-3.5"
            checked={force}
            onChange={(e) => onForce(e.target.checked)}
          />
          强制覆盖较新的一份
        </label>
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={busy !== null || selected.size === 0}
          onClick={() => onTransfer("push")}
        >
          {busy === "push" ? <IconRefresh size={12} className="animate-spin" /> : <IconUpload size={12} />}
          推送到盒子 ({Math.min(selected.size, pushable)})
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={busy !== null || selected.size === 0}
          onClick={() => onTransfer("pull")}
        >
          {busy === "pull" ? <IconRefresh size={12} className="animate-spin" /> : <IconDownload size={12} />}
          从盒子拉取 ({Math.min(selected.size, pullable)})
        </button>
      </div>

      <p className="nx-hint mt-2 text-[11px]">
        方向由按钮决定，勾选框只表示「参与这次同步」。两边都改过的条目默认不会被覆盖 ——
        要覆盖较新的一份，勾上「强制覆盖」。
      </p>
    </div>
  );
}

/** 一次同步的结果。**警告必须显示**，否则用户会以为全都同步过去了。 */
function ReportBody({ dir, data }: { dir: "push" | "pull"; data: ImportReport }) {
  const touched =
    data.assetsCreated + data.assetsUpdated + data.groupsCreated + data.credsCreated + data.credsUpdated;
  return (
    <div className={`mt-3 nx-alert ${touched > 0 ? "" : "nx-alert-danger"}`}>
      <div className="mb-1 font-semibold">
        {dir === "push" ? "推送结果" : "拉取结果"}
      </div>
      <div className="font-mono text-[11px]">
        资产 新建 {data.assetsCreated} / 更新 {data.assetsUpdated}； 凭据 新建 {data.credsCreated} / 更新{" "}
        {data.credsUpdated}； 分组 新建 {data.groupsCreated} / 更新 {data.groupsUpdated}；
        跳过（对面更新） {data.skippedNewer}； 被拒 {data.refused}
      </div>
      {data.warnings.length > 0 && (
        <ul className="mt-2 list-disc pl-4 text-[11.5px] text-amber-200">
          {data.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

// 设置页的「软件更新」卡片。
//
// 与顶部横幅（`app/UpdateBanner.tsx`）的分工：
//
//   · 横幅只在「有新版本 **且** 这个构建装得了」时出现 —— 它是**告知**，不打断。
//   · 这张卡片是**常驻入口**：想主动查、想知道当前什么版本、或者想知道
//     「为什么我这台不提示更新」（服务端构建 / 发行方还没配签名公钥），都从这里看。
//
// 后一种情况尤其需要它：那两种状态下横幅**故意不出现**（点了必然失败），
// 于是用户唯一能问到原因的地方就是这里。所以「装不了」的原因必须真的渲染出来，
// 不能吞掉。
import { useEffect } from "react";
import { useUi } from "../../app/store";
import { checkForUpdate, installUpdate } from "../../app/useUpdate";
import { IconCheckCircle, IconDownload, IconInfo, IconRefresh } from "../../ui/icons";

export function UpdateCard() {
  const info = useUi((s) => s.updateInfo);
  const installing = useUi((s) => s.updateInstalling);
  const progress = useUi((s) => s.updateProgress);

  // 打开设置页时若还没查过，就补一次。
  //
  // 结果存在 store 里（不是本组件的 state）：启动时那次静默检查、顶部横幅、
  // 这张卡片共用同一份 —— 否则光打开一次设置页就打两次 GitHub 请求，
  // 而未认证的 GitHub API 是按**出口 IP** 限流 60 次/小时的。
  useEffect(() => {
    if (!useUi.getState().updateInfo) void checkForUpdate();
  }, []);

  const pct = progress === null ? null : Math.round(progress * 100);

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconRefresh size={15} className="text-neutral-400" />
        <span className="nx-card-title">软件更新</span>
        {info?.available && <span className="nx-badge nx-badge-blue">有新版本</span>}
      </div>

      <div className="mb-3 flex items-center gap-2 text-[12.5px] text-neutral-200">
        <span>当前版本</span>
        <span className="font-mono text-neutral-400">
          {info ? `v${info.current}` : "—"}
        </span>
      </div>

      {/* 状态区：把「已是最新」「有新版本」「查不了」三种情况分开说，
          而不是统一渲染成一句"检查完成" —— 对用户这三件事的意思完全不同。 */}
      {installing ? (
        <p className="nx-hint mb-3.5">
          {pct === null ? "正在下载更新…" : `正在下载更新… ${pct}%`}
        </p>
      ) : info?.available ? (
        <p className="nx-hint mb-3.5">
          新版本 <span className="font-mono">v{info.latest}</span>
          {info.date ? ` · ${info.date}` : ""}
        </p>
      ) : info?.canInstall ? (
        <p className="mb-3.5 flex items-center gap-1.5 text-[12.5px] text-neutral-400">
          <IconCheckCircle size={13} className="text-emerald-400" />
          已是最新版本
        </p>
      ) : info ? (
        // 查不了 / 装不了：原因来自内核（服务端构建、签名公钥未配置、网络不通…）。
        // 必须展示 —— 这是用户唯一能看到它的地方。
        <p className="mb-3.5 flex items-start gap-1.5 text-[12.5px] text-amber-300/90">
          <IconInfo size={13} className="mt-0.5 shrink-0" />
          <span>{info.unavailableReason ?? "当前构建不支持应用内更新"}</span>
        </p>
      ) : (
        <p className="nx-hint mb-3.5">正在检查…</p>
      )}

      {/* 更新说明：Release 正文是 Markdown，这里按纯文本折行显示，不做渲染
          （完整排版在 Release 页；这里给的是「这次改了什么」的第一眼）。 */}
      {info?.available && info.notes && (
        <p className="nx-hint mb-3.5 max-h-24 overflow-y-auto whitespace-pre-wrap border-t border-neutral-800/60 pt-2 text-[11px]">
          {info.notes}
        </p>
      )}

      <div className="flex items-center gap-2">
        {info?.available && info.canInstall && !installing && (
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            onClick={() => void installUpdate()}
          >
            <IconDownload size={12} />
            立即更新
          </button>
        )}
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={installing}
          onClick={() => void checkForUpdate()}
        >
          <IconRefresh size={12} />
          检查更新
        </button>
      </div>
    </section>
  );
}

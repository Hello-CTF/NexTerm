// 更新提示横幅（应用级，置顶）。
//
// # 为什么是横幅，不是弹框、不是 toast
//
//   · **不是弹框**：弹框会打断用户手上正在做的事（终端里可能正跑着一条长命令），
//     而一次版本更新的紧急度远没到那个份上。横幅是「不打断」的提示 ——
//     看得到、随时能点、也能一直不理它。
//   · **不是 toast**：toast 几秒就自动消失。更新这件事用户很可能「这轮不想做、
//     下轮才想」，toast 消失之后就再也没有入口了（设置页那个入口没人会主动去找）。
//
// # 什么时候**不**出现
//
// 只有「确实有新版」**且**「这个构建装得了」才挂出来。服务端构建、或签名公钥
// 还没配时 `canInstall` 是 false —— 那时候横幅上的按钮点了必然失败，留着它就只是
// 一条赶不走的噪音。那两种情况的原因放在设置页里说（见 `UpdateCard`），
// 想让用户知道时自己去看。
import { IconClose, IconDownload } from "../ui/icons";
import { useUi } from "./store";
import { installUpdate } from "./useUpdate";

export function UpdateBanner() {
  const info = useUi((s) => s.updateInfo);
  const dismissed = useUi((s) => s.updateBannerDismissed);
  const installing = useUi((s) => s.updateInstalling);
  const progress = useUi((s) => s.updateProgress);

  if (!info?.available || !info.canInstall || dismissed) return null;

  // null = 服务端没给 Content-Length。这时显示「正在下载…」而不是「0%」——
  // 后者看着像卡死了，用户会去杀进程。
  const pct = progress === null ? null : Math.round(progress * 100);

  return (
    <div className="flex h-[34px] shrink-0 items-center gap-2.5 border-b border-blue-500/40 bg-blue-950 px-3 text-xs text-blue-100">
      <span className="flex shrink-0 items-center gap-1.5 font-semibold">
        <IconDownload size={14} className="text-blue-300" />
        发现新版本
      </span>
      <span className="nx-badge nx-badge-blue shrink-0">v{info.latest}</span>
      <span className="shrink-0 text-blue-300/70">当前 v{info.current}</span>

      {/* 更新说明（Release 正文是 Markdown，这里只做单行预览；完整内容在 Release 页） */}
      {info.notes ? (
        <span className="min-w-0 flex-1 truncate text-blue-200/80" title={info.notes}>
          {info.notes}
        </span>
      ) : (
        <span className="min-w-0 flex-1" />
      )}

      {installing ? (
        <span className="shrink-0 font-mono tabular-nums text-blue-200">
          {pct === null ? "正在下载…" : `正在下载 ${pct}%`}
        </span>
      ) : (
        <button
          className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
          onClick={() => void installUpdate()}
        >
          <IconDownload size={12} />
          立即更新
        </button>
      )}
      <button
        className="nx-icon-btn nx-icon-btn-sm shrink-0"
        title="稍后（本次运行不再提示）"
        disabled={installing}
        onClick={() => useUi.getState().dismissUpdateBanner()}
      >
        <IconClose size={11} />
      </button>
    </div>
  );
}

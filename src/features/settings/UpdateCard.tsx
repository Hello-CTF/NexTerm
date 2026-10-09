import { DESKTOP } from "../../ipc/env";
import { DEMO, WEB } from "../../demo";
import { formatBytes } from "../../ui/format";
import {
  IconCheckCircle,
  IconDownload,
  IconLoader,
  IconRefresh,
  IconRestart,
  IconXCircle,
} from "../../ui/icons";
import { updateInstallAllowed, updateVerdict, useUpdate, type UpdateVerdict } from "../../app/useUpdate";

function verdictBadge(verdict: UpdateVerdict): { text: string; tone: string } {
  switch (verdict) {
    case "checking":
      return { text: "检查中", tone: "" };
    case "uptodate":
      return { text: "已是最新", tone: "nx-badge-green" };
    case "available":
      return { text: "有更新", tone: "nx-badge-green" };
    case "unavailable":
      return { text: "有更新 · 不可安装", tone: "nx-badge-amber" };
    case "check-failed":
      return { text: "检查失败", tone: "nx-badge-red" };
    default:
      return { text: "未检查", tone: "" };
  }
}

export function UpdateCard() {
  const update = useUpdate();
  const { status, checking, checkError, install, installedVersion, restarting, restartError } =
    update;
  const verdict = updateVerdict(update);
  const badge = verdictBadge(verdict);

  const envReason = DEMO
    ? "演示模式不执行真实安装"
    : WEB
      ? "服务端模式不支持应用内安装，请到 Releases 页面手动下载"
      : null;
  const installAllowed = updateInstallAllowed(status);
  const blockReason =
    (verdict === "available" || verdict === "unavailable") && !installAllowed
      ? status?.unavailableReason || envReason || "当前环境不支持应用内安装"
      : null;
  const showInstall =
    verdict === "available" && installAllowed && !install.active && !installedVersion;
  const percent =
    install.total > 0 ? Math.min(100, Math.round((install.transferred / install.total) * 100)) : 0;
  const failReason = checkError || status?.unavailableReason || "原因未查明";

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconDownload size={15} className="text-neutral-400" />
        <span className="nx-card-title">软件更新</span>
        <span className={`nx-badge ${badge.tone}`}>{badge.text}</span>
      </div>

      <p className="nx-hint mb-3">
        当前版本 {status?.currentVersion ? `v${status.currentVersion}` : "…"}
        {verdict === "uptodate" ? " · 已是最新版本" : ""}
      </p>

      {verdict === "available" && status && (
        <p className="mb-2 text-[12.5px] text-neutral-200">
          发现新版本 v{status.version}
          {status.assetSize > 0 ? ` · 安装包 ${formatBytes(status.assetSize)}` : ""}
          {status.prerelease ? " · 预发布" : ""}
        </p>
      )}
      {blockReason && (
        <p className="mb-2 text-[12px] text-amber-300/90">{blockReason}</p>
      )}
      {verdict === "check-failed" && (
        <div className="nx-alert nx-alert-danger mb-2 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">检查失败:{failReason}</span>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={checking}
          onClick={() => void update.check()}
        >
          {checking ? (
            <IconLoader size={12} className="animate-spin" />
          ) : (
            <IconRefresh size={12} />
          )}
          {checking ? "正在检查…" : "立即检查"}
        </button>
        {showInstall && (
          <button
            type="button"
            className="nx-btn nx-btn-primary nx-btn-sm"
            onClick={() => void update.installUpdate()}
          >
            <IconDownload size={12} />
            下载并安装
          </button>
        )}
      </div>

      {install.active && (
        <div className="mt-3">
          <div
            className="nx-progress"
            role="progressbar"
            aria-label={install.phase === "install" ? "安装进度" : "下载进度"}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={install.total > 0 ? percent : undefined}
          >
            {install.total > 0 ? (
              <span className="nx-progress-bar" style={{ width: `${percent}%` }} />
            ) : (
              <span className="nx-progress-bar w-1/3 animate-pulse motion-reduce:animate-none" />
            )}
          </div>
          <p className="nx-hint mt-1">
            {install.phase === "install" ? "正在安装…" : "正在下载…"}
            {install.total > 0
              ? ` ${percent}% · ${formatBytes(install.transferred)} / ${formatBytes(install.total)}`
              : " 进度未知"}
          </p>
        </div>
      )}

      {install.error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">安装失败:{install.error}</span>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            onClick={() => void update.installUpdate()}
          >
            重试
          </button>
        </div>
      )}

      {installedVersion && (
        <div className="nx-alert mt-3 flex items-start gap-2 border-green-500/40 bg-[color-mix(in_srgb,var(--color-green-500)_18%,var(--nx-bg-pane))] text-green-300">
          <IconCheckCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1">
            已安装 v{installedVersion}，重启应用后生效。
            {restartError && <span className="mt-0.5 block text-red-300">重启失败:{restartError}</span>}
          </span>
          {DESKTOP && (
            <button
              type="button"
              className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
              disabled={restarting}
              onClick={() => void update.restart()}
            >
              <IconRestart size={12} />
              {restarting ? "正在重启…" : "立即重启"}
            </button>
          )}
        </div>
      )}
    </section>
  );
}

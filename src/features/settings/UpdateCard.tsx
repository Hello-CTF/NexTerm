import { DESKTOP } from "../../ipc/env";
import { WEB } from "../../ipc/env";
import { DownloadProgress } from "../../ui/DownloadProgress";
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
    case "external":
      return { text: "不在应用内更新", tone: "" };
    case "check-failed":
      return { text: "检查失败", tone: "nx-badge-red" };
    default:
      return { text: "未检查", tone: "" };
  }
}

// externalChannelNote says where updates come from on platforms that cannot install them inside the app.
function externalChannelNote(channel: string | undefined): string {
  if (channel === "app-store") return "iOS / iPadOS 版本通过 TestFlight 或 App Store 更新，请在对应的应用里查看新版本。";
  return "此平台不支持应用内更新，请到 Releases 页面下载新的安装包。";
}

export function UpdateCard() {
  const update = useUpdate();
  const { status, checking, checkError, install, installedVersion, restarting, restartError } =
    update;
  const verdict = updateVerdict(update);
  const badge = verdictBadge(verdict);

  const envReason = WEB
    ? "服务端模式不支持应用内安装，请到 Releases 页面手动下载"
    : null;
  const installAllowed = updateInstallAllowed(status);
  const blockReason =
    (verdict === "available" || verdict === "unavailable") && !installAllowed
      ? status?.unavailableReason || envReason || "当前环境不支持应用内安装"
      : null;
  const showInstall =
    verdict === "available" && installAllowed && !install.active && !installedVersion;
  const failReason = checkError || status?.unavailableReason || "原因不明";

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
      {verdict === "external" && (
        <p className="mb-2 text-[12.5px] text-neutral-300">{externalChannelNote(status?.channel)}</p>
      )}
      {blockReason && (
        <p className="mb-2 text-[12px] text-amber-300">{blockReason}</p>
      )}
      {verdict === "check-failed" && (
        <div className="nx-alert nx-alert-danger mb-2 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">未能检查更新：{failReason}。请检查网络后再点「立即检查」。</span>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {verdict !== "external" && (
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={checking}
            aria-busy={checking}
            title={checking ? "正在检查更新，请稍候" : undefined}
            onClick={() => void update.check()}
          >
            {checking ? (
              <IconLoader size={12} className="animate-spin" />
            ) : (
              <IconRefresh size={12} />
            )}
            {checking ? "正在检查…" : "立即检查"}
          </button>
        )}
        {showInstall && (
          <button
            type="button"
            className="nx-btn nx-btn-success nx-btn-sm"
            onClick={() => void update.installUpdate()}
          >
            <IconDownload size={12} />
            下载并安装
          </button>
        )}
      </div>

      {install.active && (
        <DownloadProgress className="mt-3" label={install.phase === "install" ? "正在安装…" : "正在下载…"}
          transferred={install.transferred} total={install.total} />
      )}

      {install.error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">安装失败：{install.error}。可选择「重试」，仍失败请到 Releases 页面手动下载。</span>
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
        <div className="nx-alert nx-alert-success mt-3 flex items-start gap-2">
          <IconCheckCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1">
            已安装 v{installedVersion}，重启应用后生效。
            {restartError && <span className="mt-0.5 block text-red-300">重启失败：{restartError}。请手动退出并重新打开应用。</span>}
          </span>
          {DESKTOP && (
            <button
              type="button"
              className="nx-btn nx-btn-success nx-btn-sm shrink-0"
              disabled={restarting}
              aria-busy={restarting}
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

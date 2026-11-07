import { DESKTOP } from "../../ipc/env";
import { WEB } from "../../ipc/env";
import { confirmDialog } from "../../ui/dialogs";
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
import type { UpdateReleaseChannel } from "../../ipc/types";
import {
  effectiveReleaseChannel,
  updateInstallAllowed,
  updateVerdict,
  useUpdate,
  type UpdateVerdict,
} from "../../app/useUpdate";
import { t } from "../../i18n";

const CHANNEL_OPTIONS: ReadonlyArray<{ value: UpdateReleaseChannel; label: string }> = [
  { value: "stable", label: t("稳定版") },
  { value: "rc", label: "RC" },
];

const RC_RISK_NOTE =
  t("RC 是正式发布前的候选版本，可能包含尚未发现的问题。此设置只对本机生效，不会同步到其他设备。切换回稳定版不会降级，会保持当前版本，直到有更新的稳定版发布。确定切换到 RC 通道吗？");

function verdictBadge(verdict: UpdateVerdict): { text: string; tone: string } {
  switch (verdict) {
    case "checking":
      return { text: t("检查中"), tone: "" };
    case "uptodate":
      return { text: t("已是最新"), tone: "nx-badge-green" };
    case "available":
      return { text: t("有更新"), tone: "nx-badge-green" };
    case "unavailable":
      return { text: t("有更新 · 不可安装"), tone: "nx-badge-amber" };
    case "external":
      return { text: t("不支持应用内更新"), tone: "" };
    case "check-failed":
      return { text: t("检查失败"), tone: "nx-badge-red" };
    default:
      return { text: t("未检查"), tone: "" };
  }
}

// externalChannelNote says where updates come from on platforms that cannot install them inside the app.
function externalChannelNote(channel: string | undefined): string {
  if (channel === "app-store") return t("iOS / iPadOS 版本通过 TestFlight 或 App Store 更新，请在对应的应用中查看新版本。");
  return t("此平台不支持应用内更新，请前往 Releases 页面下载新的安装包。");
}

export function UpdateCard() {
  const update = useUpdate();
  const { status, checking, checkError, install, installedVersion, restarting, restartError } =
    update;
  const verdict = updateVerdict(update);
  const badge = verdictBadge(verdict);

  const envReason = WEB
    ? t("服务端模式不支持应用内安装，请前往 Releases 页面手动下载")
    : null;
  const installAllowed = updateInstallAllowed(status);
  const blockReason =
    (verdict === "available" || verdict === "unavailable") && !installAllowed
      ? status?.unavailableReason || envReason || t("当前环境不支持应用内安装")
      : null;
  const showInstall =
    verdict === "available" && installAllowed && !install.active && !installedVersion;
  const failReason = checkError || status?.unavailableReason || t("原因未知");
  const channel = effectiveReleaseChannel(update);
  // The channel only applies where the app installs updates itself: not in server mode, not on iOS or Android.
  const showChannel = DESKTOP && verdict !== "external";
  const runningRC = status?.runningPrerelease === true;

  const chooseChannel = async (next: UpdateReleaseChannel) => {
    if (next === channel || update.channelSaving) return;
    if (next === "rc" && !(await confirmDialog(RC_RISK_NOTE))) return;
    await update.setReleaseChannel(next);
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconDownload size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("软件更新")}</span>
        <span className={`nx-badge ${badge.tone}`}>{badge.text}</span>
      </div>

      <p className="nx-hint mb-3">
        {t("当前版本 {value}{value2}{value3}", { value: status?.currentVersion ? `v${status.currentVersion}` : "…", value2: verdict === "uptodate" ? t(" · 已是最新版本") : "", value3: " · " })}
        <a href="/THIRD_PARTY_NOTICES.txt" target="_blank" rel="noreferrer" className="underline">{t("第三方许可")}</a>
      </p>

      {showChannel && (
        <div className="mb-3" data-setting="update-channel">
          <div className="mb-1 flex flex-wrap items-center gap-2">
            <span className="text-[12.5px] text-neutral-200">{t("更新通道")}</span>
            <div className="nx-segment" role="group" aria-label={t("更新通道")}>
              {CHANNEL_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={`nx-segment-item ${channel === option.value ? "is-active" : ""}`}
                  aria-pressed={channel === option.value}
                  disabled={update.channelSaving}
                  onClick={() => void chooseChannel(option.value)}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </div>
          <p className="nx-hint">
            {channel === "rc"
              ? t("RC 通道会收到正式发布前的候选版本，也会收到稳定版。仅对本机生效。")
              : t("稳定版通道只接收正式发布的版本。仅对本机生效。")}
          </p>
          {channel === "stable" && runningRC && (
            <p className="nx-hint mt-1 text-amber-300">
              {t("当前运行的是 RC 版本。选择稳定版不会降级，将保持当前版本，直到有更新的稳定版发布。")}
            </p>
          )}
          {update.channelError && (
            <p className="mt-1 text-[12px] text-red-300">{t("更新通道保存失败：{channelError}", { channelError: update.channelError })}</p>
          )}
        </div>
      )}

      {verdict === "available" && status && (
        <p className="mb-2 text-[12.5px] text-neutral-200">
          {t("发现新版本 v{version}{value}{value2}", { version: status.version, value: status.assetSize > 0 ? t(" · 安装包 {value}", { value: formatBytes(status.assetSize) }) : "", value2: status.prerelease ? t(" · RC 候选版本") : "" })}
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
          <span className="min-w-0 flex-1 break-words">{t("未能检查更新：{failReason}。请检查网络后选择「立即检查」。", { failReason })}</span>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {verdict !== "external" && (
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={checking}
            aria-busy={checking}
            title={checking ? t("正在检查更新，请稍候") : undefined}
            onClick={() => void update.check()}
          >
            {checking ? (
              <IconLoader size={12} className="animate-spin" />
            ) : (
              <IconRefresh size={12} />
            )}
            {checking ? t("正在检查…") : t("立即检查")}
          </button>
        )}
        {showInstall && (
          <button
            type="button"
            className="nx-btn nx-btn-success nx-btn-sm"
            onClick={() => void update.installUpdate()}
          >
            <IconDownload size={12} />
            {t("下载并安装")}
          </button>
        )}
      </div>

      {install.active && (
        <DownloadProgress className="mt-3" label={install.phase === "install" ? t("正在安装…") : t("正在下载…")}
          transferred={install.transferred} total={install.total} />
      )}

      {install.error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{t("安装失败：{error}。可选择「重试」，仍失败，请前往 Releases 页面手动下载。", { error: install.error })}</span>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            onClick={() => void update.installUpdate()}
          >
            {t("重试")}
          </button>
        </div>
      )}

      {installedVersion && (
        <div className="nx-alert nx-alert-success mt-3 flex items-start gap-2">
          <IconCheckCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1">
            {t("已安装 v{installedVersion}，重启应用后生效。", { installedVersion })}
            {restartError && <span className="mt-0.5 block text-red-300">{t("重启失败：{restartError}。请手动退出并重新打开应用。", { restartError })}</span>}
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
              {restarting ? t("正在重启…") : t("立即重启")}
            </button>
          )}
        </div>
      )}
    </section>
  );
}

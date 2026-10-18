import { useState } from "react";
import { syncApi } from "../../ipc/commands";
import { openExternalURL } from "../../ipc/wails";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { IconCopy } from "../../ui/icons";

function securitySettingsURL(raw: string): string {
  const url = new URL(raw);
  if (!(["http:", "https:"].includes(url.protocol)) || !url.hostname || url.username || url.password || url.search || url.hash) {
    throw new Error("服务端地址无效，请在账号同步中检查连接地址");
  }
  url.hash = "settings-account";
  return url.toString();
}

export function ServerSecurityLink() {
  const [busy, setBusy] = useState(false);
  const [fallbackURL, setFallbackURL] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const open = async () => {
    setBusy(true);
    setError(null);
    setFallbackURL(null);
    try {
      const link = await syncApi.linkGet();
      const url = securitySettingsURL(link.url);
      try {
        await openExternalURL(url);
      } catch (e) {
        setFallbackURL(url);
        setError(`浏览器打开失败：${describeError(e)}。请复制地址后手动打开。`);
      }
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-2">
      <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={busy} onClick={() => void open()}>
        {busy ? "正在打开…" : "打开服务端安全设置"}
      </button>
      {error && <p className="nx-hint mt-2 text-amber-300" role="alert">{error}</p>}
      {fallbackURL && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <input className="nx-input min-w-0 flex-1 font-mono" aria-label="服务端安全设置地址" value={fallbackURL} readOnly onFocus={(e) => e.currentTarget.select()} />
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            onClick={() => void copyText(fallbackURL).catch((e: unknown) => setError(`复制失败：${describeError(e)}。请手动选中地址复制。`))}
          >
            <IconCopy size={12} />
            复制地址
          </button>
        </div>
      )}
    </div>
  );
}

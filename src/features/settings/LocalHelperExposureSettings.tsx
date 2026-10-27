import { useEffect, useState } from "react";
import { localHelperApi, type LocalHelperExposure } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";

const STATE_TEXT: Record<string, string> = {
  off: "未开放",
  unavailable: "暂不可用",
  enrolling: "正在注册",
  connecting: "未连接服务器",
  online: "已连接",
  revoked: "已被吊销",
  failed: "出错",
};

// The status changes on its own while exposure is on (connecting, online, revoked), so it is polled while shown.
const POLL_MS = 5000;

// LocalHelperExposureSettings lets this desktop expose its local terminals to the server account it is linked to,
// so that account's browsers and phones can list and attach them. It renders nothing where exposure is unsupported.
export function LocalHelperExposureSettings() {
  const pushToast = useUi((s) => s.pushToast);
  const [status, setStatus] = useState<LocalHelperExposure | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setInterval(() => void load(), POLL_MS);
    const load = () =>
      Promise.resolve()
        .then(() => localHelperApi.getExposure())
        .then((value) => {
          if (cancelled) return;
          setStatus(value);
          // Where exposure is unsupported it never changes.
          if (!value.enabled && value.state === "unavailable") window.clearInterval(timer);
        })
        .catch(() => {
          if (cancelled) return;
          setStatus(null);
          window.clearInterval(timer);
        });
    void load();
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  if (!status || (!status.enabled && status.state === "unavailable")) return null;

  const toggle = async () => {
    if (busy) return;
    const enabling = !status.enabled;
    if (enabling) {
      const ok = await ask(
        "开放后，登录同一服务器账号的浏览器和手机可以查看、接管并新开这台电脑上的本机终端，相当于远程使用这台电脑的命令行。\n只有当前同步账号可以访问，不能分享给其他账号；可随时关闭，或在设备列表中吊销。仍要开放？",
        { title: "向服务器开放本机终端", kind: "warning" },
      );
      if (!ok) return;
    }
    setBusy(true);
    try {
      setStatus(await localHelperApi.setExposure(enabling));
    } catch (error) {
      pushToast("error", `${enabling ? "开放" : "关闭"}本机终端失败：${describeError(error)}`);
      try {
        setStatus(await localHelperApi.getExposure());
      } catch {
        // Keep the last status.
      }
    } finally {
      setBusy(false);
    }
  };

  const badge = status.state === "online" ? "nx-badge nx-badge-green" : status.state === "off" ? "nx-badge" : "nx-badge nx-badge-amber";
  return (
    <label className="mb-3.5 flex cursor-pointer items-start gap-3" data-local-helper-exposure={status.state}>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 text-[12.5px] text-neutral-200">
          向服务器账号开放本机终端
          <span className={badge}>{STATE_TEXT[status.state] ?? status.state}</span>
        </div>
        <p className="nx-hint mt-1">
          开放后，同一服务器账号的浏览器和手机可以查看并接管这台电脑的本机持久终端；其他账号无法访问。
        </p>
        {status.reason && <p className="nx-hint mt-1 text-amber-300">{status.reason}</p>}
      </div>
      <input
        type="checkbox"
        className="mt-0.5 h-4 w-4 shrink-0"
        aria-label="向服务器账号开放本机终端"
        checked={status.enabled}
        disabled={busy}
        onChange={() => void toggle()}
      />
    </label>
  );
}

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
  revoked: "已吊销",
  failed: "出现错误",
};

// What exposure hands over, stated plainly: the confirmation and the hint must not understate it.
const EXPOSE_CONFIRM = [
  "开放后，相当于将本机当前用户的命令行权限交给已同步的服务器账号：该服务器的运维人员或管理员，以及任何能够控制该服务器或该账号的人，均可以当前用户身份在本机执行任意命令，包括通过该账号的 AI 助手（含定时任务）和批量执行。",
  "「不能分享给其他账号」仅表示分享功能不可用，并不限制上述人员。",
  "从远程打开的终端在关闭开放后仍会继续运行，需在「后台会话」中结束；锁定凭据库不会停止开放。设备还将定期向服务器上报 CPU、内存、磁盘和运行时长。",
  "确定要开放吗？",
].join("\n");

const EXPOSE_HINT =
  "开放后，服务器的运维人员、管理员，以及能控制该服务器或账号的人（含该账号的 AI 助手、定时任务和批量执行），可以当前用户身份在本机执行任意命令；本机同时向服务器上报运行指标。关闭开放不会结束已打开的远程终端，需在「后台会话」中结束；锁定凭据库不会停止开放。";

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
      const ok = await ask(EXPOSE_CONFIRM, { title: "向服务器开放本机终端", kind: "warning" });
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
        <p className="nx-hint mt-1">{EXPOSE_HINT}</p>
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

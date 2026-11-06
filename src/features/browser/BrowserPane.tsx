import { useCallback, useEffect, useRef, useState } from "react";
import { isMobileShell } from "../../app/shell/shellMode";
import { useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { previewApi } from "../../ipc/previewApi";
import { openExternalURL } from "../../ipc/wails";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { currentEmbedEnv, planEmbed, sandboxFor } from "./embedPolicy";

const LOAD_TIMEOUT_MS = 10_000;
const BLOCKED_TOAST = "该网站不允许嵌入，已在系统浏览器打开";

async function openSystem(url: string): Promise<void> {
  if (WEB) {
    // A blocked popup returns null; that is not an error the caller can act on.
    window.open(url, "_blank", "noopener,noreferrer");
    return;
  }
  await openExternalURL(url);
}

type Notice = { kind: "blocked"; text: string } | { kind: "unreachable"; reason: string } | { kind: "timeout" } | null;

function readFrameUrl(frame: HTMLIFrameElement | null): string | null {
  try {
    const href = frame?.contentWindow?.location.href;
    return href && /^https?:/i.test(href) ? href : null;
  } catch {
    return null;
  }
}

export function BrowserPane({ url }: { url: string }) {
  const plan = planEmbed(url, currentEmbedEnv(WEB, isMobileShell()));
  const embedUrl = plan.kind === "embed" ? plan.url : null;
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const loads = useRef(0);
  const autoOpened = useRef(false);
  const [address, setAddress] = useState(url);
  const [moved, setMoved] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);

  const fallback = useCallback((initial: string, next: Notice, toast: string) => {
    setNotice(next);
    if (autoOpened.current) return;
    autoOpened.current = true;
    useUi.getState().pushToast("info", toast);
    // Auto-open is best effort: the visible button remains if it fails.
    void openSystem(initial).catch(() => undefined);
  }, []);

  useEffect(() => {
    loads.current = 0;
    autoOpened.current = false;
    setAddress(url);
    setMoved(false);
    setNotice(null);
    if (!embedUrl) return;
    let alive = true;
    const timer = setTimeout(() => {
      if (alive && loads.current === 0) fallback(embedUrl, { kind: "timeout" }, `${BLOCKED_TOAST}（加载超时）`);
    }, LOAD_TIMEOUT_MS);
    if (!WEB) {
      void previewApi.embedCheck(embedUrl).then(
        (result) => {
          if (!alive) return;
          if (result.status === "blocked") {
            clearTimeout(timer);
            fallback(embedUrl, { kind: "blocked", text: result.reason ?? "" }, BLOCKED_TOAST);
          } else if (result.status === "unreachable") {
            clearTimeout(timer);
            setNotice({ kind: "unreachable", reason: result.reason ?? "" });
          }
        },
        () => undefined,
      );
    }
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [url, embedUrl, fallback]);

  const onLoad = () => {
    loads.current += 1;
    if (loads.current === 1) return;
    setMoved(true);
    const current = readFrameUrl(frameRef.current);
    if (current) setAddress(current);
  };

  const openInSystem = () => {
    void openSystem(url).catch((e: unknown) => useUi.getState().pushToast("error", `无法打开链接：${describeError(e)}`));
  };
  const copy = () => {
    void copyText(address).then(
      () => useUi.getState().pushToast("success", "已复制地址"),
      (e: unknown) => useUi.getState().pushToast("error", `复制失败：${describeError(e)}`),
    );
  };

  const hideFrame = !embedUrl || notice?.kind === "blocked" || notice?.kind === "unreachable";
  const addressFollowed = address !== url;
  return (
    <div className="nx-pane flex h-full flex-col">
      <div className="nx-toolbar flex flex-wrap items-center gap-2">
        <input
          className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
          aria-label="地址"
          readOnly
          value={address}
        />
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" title={`打开初始地址：${url}`} onClick={openInSystem}>
          在系统浏览器打开
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={copy}>
          复制
        </button>
      </div>
      <p className="nx-hint px-3 py-1.5">
        用于开发预览和文档页；网站拒绝嵌入时改用系统浏览器打开。
      </p>
      {WEB && (
        <p className="nx-hint flex flex-wrap items-center gap-2 px-3 pb-1.5">
          只能嵌入 https 页面；若页面空白，该网站可能不允许嵌入。
          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" onClick={openInSystem}>
            在系统浏览器打开
          </button>
        </p>
      )}
      {moved && !addressFollowed && (
        <p className="nx-hint px-3 pb-1.5">页面已在框架内跳转，跨源页面无法读取当前地址。</p>
      )}
      {plan.kind !== "embed" && (
        <div className="px-3 py-3 text-sm text-neutral-400" role="status">
          {plan.reason}。可以用系统浏览器打开。
        </div>
      )}
      {notice?.kind === "blocked" && (
        <div className="px-3 py-3 text-sm text-neutral-400" role="status">
          {BLOCKED_TOAST}。
        </div>
      )}
      {notice?.kind === "unreachable" && (
        <div className="px-3 py-3 text-sm text-neutral-400" role="status">
          无法连接该地址{notice.reason ? `：${notice.reason}` : ""}
        </div>
      )}
      {notice?.kind === "timeout" && (
        <div className="px-3 py-1.5 text-sm text-neutral-400" role="status">
          加载超时，已尝试在系统浏览器打开。
        </div>
      )}
      {!hideFrame && embedUrl && (
        <iframe
          ref={frameRef}
          key={embedUrl}
          className="min-h-0 w-full flex-1 border-0 bg-white"
          src={embedUrl}
          sandbox={sandboxFor(embedUrl, currentEmbedEnv(WEB, false).appOrigin)}
          referrerPolicy="no-referrer"
          title={`预览：${url}`}
          onLoad={onLoad}
        />
      )}
    </div>
  );
}

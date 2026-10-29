// Preview link creation entry (session/terminal share menu): creates an anonymously openable preview link for the current terminal tab.
// Read-only by default: viewers see the current screen and subsequent live output but cannot input; on WEB, an "allow control" checkbox
// grants viewers input permission (write is sent with the create request). Links expire and can be revoked.
// This is a different entry from host shares (host_share, between logged-in accounts) and device public links. The token is held only
// at creation success to assemble the public URL - never logged, persisted, or put into web storage.
// Data sources switch by TRANSPORT: WEB talks to previewApi directly (server-hosted tab with the same id); desktop goes through the
// remote relay - create via preview_share_create (remote shell + link + output relay), the list filters remoteSharingApi.previewLinks by the
// current tab's active relay shell id, and revoke likewise goes remote.
// Previews created by the desktop relay do not support control: the checkbox is disabled and permanently read-only.

import { useCallback, useEffect, useRef, useState } from "react";
import {
  createPreviewLink,
  listPreviewLinks,
  previewPublicUrl,
  revokePreviewLink,
  type PreviewLinkView,
} from "./previewApi";
import { WEB } from "../../ipc/env";
import { remotePreviewApi, remoteSharingApi } from "../../ipc/remoteApi";
import { describeError } from "../../ui/errorText";
import { copyText } from "../../ui/clipboard";
import { Select } from "../../ui/Select";
import { IconGlobe } from "../../ui/icons";

// All TTL options fall within the server-side minShareTTL(1 minute)-maxShareTTL(30 days) bounds;
// the 1h default aligns with the server defaultLinkTTL, and ttl_ms is always sent explicitly on create.
const PREVIEW_TTL_OPTIONS = [
  { label: "15 分钟", ms: 15 * 60_000 },
  { label: "1 小时", ms: 60 * 60_000 },
  { label: "6 小时", ms: 6 * 60 * 60_000 },
  { label: "24 小时", ms: 24 * 60 * 60_000 },
  { label: "7 天", ms: 7 * 24 * 60 * 60_000 },
];

function activePreviews(links: PreviewLinkView[], tabId: string): PreviewLinkView[] {
  const now = Date.now();
  return links.filter((link) => link.session_id === tabId && !link.revoked_at && link.expires_at > now);
}

// Active links may carry a permission field (read/read_write); missing or unknown values are always displayed as read-only.
function previewPermission(link: PreviewLinkView): "read" | "read_write" {
  const raw = (link as PreviewLinkView & { permission?: unknown }).permission;
  return raw === "read_write" ? "read_write" : "read";
}

interface PreviewShareMenuProps {
  tabId: string;
  sessionName?: string;
  // The toolbar overflow menu opens the panel by increasing this counter; the standalone button is hidden on narrow
  // panes, where the overflow menu carries the entry instead.
  openRequest?: number;
  // Reports whether the tab has an active preview link, so the host can keep the sharing state visible. The list is
  // also loaded once on mount when this is set.
  onActiveChange?: (active: boolean) => void;
}

export function PreviewShareMenu({ tabId, sessionName, openRequest, onActiveChange }: PreviewShareMenuProps) {
  const [open, setOpen] = useState(false);
  const [ttlMs, setTtlMs] = useState(PREVIEW_TTL_OPTIONS[1].ms);
  const [allowWrite, setAllowWrite] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [createdUrl, setCreatedUrl] = useState<string | null>(null);
  const [copyHint, setCopyHint] = useState<string | null>(null);
  const [links, setLinks] = useState<PreviewLinkView[]>([]);
  const epochRef = useRef(0);
  const unmountedRef = useRef(false);
  const copyHintTimerRef = useRef<number | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    unmountedRef.current = false;
    return () => {
      unmountedRef.current = true;
      epochRef.current += 1;
      if (copyHintTimerRef.current !== null) window.clearTimeout(copyHintTimerRef.current);
    };
  }, []);

  const refresh = useCallback(async () => {
    const epoch = epochRef.current;
    try {
      if (WEB) {
        const result = await listPreviewLinks();
        if (epoch !== epochRef.current || unmountedRef.current) return;
        setLinks(activePreviews(result.links ?? [], tabId));
      } else {
        // Desktop: links hang off remote shells; query the current tab's active relay shell id first, then filter.
        const active = await remotePreviewApi.active(tabId);
        if (!active.sessionId) {
          if (epoch !== epochRef.current || unmountedRef.current) return;
          setLinks([]);
          return;
        }
        const result = await remoteSharingApi.previewLinks();
        if (epoch !== epochRef.current || unmountedRef.current) return;
        setLinks(activePreviews(result.links ?? [], active.sessionId));
      }
    } catch {
      // A list failure does not break the create entry; the next panel open retries.
    }
  }, [tabId]);

  useEffect(() => {
    if (open || onActiveChange) void refresh();
  }, [open, onActiveChange, refresh]);

  const hasActive = links.length > 0;
  useEffect(() => {
    onActiveChange?.(hasActive);
  }, [hasActive, onActiveChange]);

  const handledOpenRequest = useRef(openRequest ?? 0);
  useEffect(() => {
    if (openRequest === undefined || openRequest === handledOpenRequest.current) return;
    handledOpenRequest.current = openRequest;
    setOpen(true);
  }, [openRequest]);

  const close = useCallback(() => {
    epochRef.current += 1;
    setOpen(false);
    setCreating(false);
    setCreatedUrl(null);
    setError(null);
  }, []);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) close();
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open, close]);

  const toggle = useCallback(() => {
    if (open) {
      close();
      return;
    }
    setOpen(true);
  }, [open, close]);

  const create = useCallback(async () => {
    if (creating) return;
    const epoch = epochRef.current;
    setCreating(true);
    setError(null);
    try {
      if (WEB) {
        const link = await createPreviewLink({ sessionId: tabId, ttlMs, write: allowWrite });
        if (epoch !== epochRef.current || unmountedRef.current) return;
        setCreatedUrl(previewPublicUrl(link.token));
      } else {
        // Previews created by the desktop relay do not support control; the create signature stays (tabId, ttlMs).
        const created = await remotePreviewApi.create(tabId, ttlMs);
        if (epoch !== epochRef.current || unmountedRef.current) return;
        setCreatedUrl(created.url);
      }
      setAllowWrite(false);
      void refresh();
    } catch (e) {
      if (epoch !== epochRef.current || unmountedRef.current) return;
      setError(describeError(e));
    } finally {
      if (epoch === epochRef.current && !unmountedRef.current) setCreating(false);
    }
  }, [creating, tabId, ttlMs, allowWrite, refresh]);

  const revoke = useCallback(
    async (id: string) => {
      try {
        if (WEB) {
          await revokePreviewLink(id);
        } else {
          await remoteSharingApi.revokePreviewLink(id);
        }
      } catch (e) {
        if (!unmountedRef.current) setError(describeError(e));
      }
      if (!unmountedRef.current) void refresh();
    },
    [refresh],
  );

  const copyUrl = useCallback(async (url: string) => {
    if (copyHintTimerRef.current !== null) window.clearTimeout(copyHintTimerRef.current);
    try {
      await copyText(url);
      setCopyHint("链接已复制");
    } catch {
      setCopyHint("复制失败，请手动复制链接");
    }
    copyHintTimerRef.current = window.setTimeout(() => {
      copyHintTimerRef.current = null;
      setCopyHint(null);
    }, 1500);
  }, []);

  return (
    <div ref={rootRef} className="relative inline-block">
      <button
        type="button"
        title="创建预览链接"
        className="flex items-center gap-1 rounded-md px-2 py-1 text-xs max-sm:min-h-10 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200 @max-[560px]:hidden"
        onClick={toggle}
      >
        <IconGlobe className="h-3.5 w-3.5" />
        预览链接
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-80 max-w-[calc(100vw-1.5rem)] @max-[560px]:fixed @max-[560px]:inset-x-3 @max-[560px]:top-14 @max-[560px]:w-auto rounded-md border border-neutral-700 bg-neutral-900 p-3 shadow-xl">
          <p className="text-xs text-neutral-300">
            为「{sessionName || "当前终端"}」创建预览链接：获得链接的人无需登录即可查看当前屏幕和后续输出。默认只读，观看者不能输入。
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Select
              className="w-32 shrink-0"
              ariaLabel="预览链接有效期"
              value={String(ttlMs)}
              onChange={(value) => setTtlMs(Number(value))}
              options={PREVIEW_TTL_OPTIONS.map((option) => ({ value: String(option.ms), label: option.label }))}
            />
            <button
              type="button"
              className="rounded-md bg-indigo-600 px-3 py-1 text-xs max-sm:min-h-10 text-white hover:bg-indigo-500 disabled:opacity-50"
              disabled={creating}
              onClick={() => void create()}
            >
              {creating ? "正在创建…" : "创建链接"}
            </button>
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <label className="flex items-center gap-1.5 text-xs text-neutral-300">
              <input
                type="checkbox"
                checked={allowWrite}
                disabled={!WEB}
                onChange={(event) => setAllowWrite(event.target.checked)}
              />
              允许控制
            </label>
            {!WEB && <span className="text-xs text-neutral-500">桌面中继创建的预览暂不支持控制</span>}
          </div>
          {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
          {createdUrl && (
            <div className="mt-2 rounded-md border border-green-500/20 bg-green-500/10 p-2">
              <p className="break-all text-xs text-green-300">{createdUrl}</p>
              <button
                type="button"
                className="mt-1 rounded-md bg-neutral-800 px-2 py-0.5 text-xs max-sm:min-h-9 text-neutral-200 hover:bg-neutral-700"
                onClick={() => void copyUrl(createdUrl)}
              >
                复制链接
              </button>
            </div>
          )}
          {links.length > 0 && (
            <div className="mt-3 border-t border-neutral-800 pt-2">
              <p className="text-xs text-neutral-500">进行中的预览</p>
              {links.map((link) => {
                const permission = previewPermission(link);
                return (
                  <div key={link.id} className="mt-1 flex flex-wrap items-center justify-between gap-1 text-xs">
                    <span className="text-neutral-400">有效期至 {new Date(link.expires_at).toLocaleString()}</span>
                    <span className="flex items-center gap-2">
                      <span
                        className={
                          permission === "read_write"
                            ? "nx-badge nx-badge-green"
                            : "rounded-full bg-neutral-800 px-2 py-0.5 text-neutral-400"
                        }
                      >
                        {permission === "read_write" ? "可控制" : "只读"}
                      </span>
                      <button
                        type="button"
                        className="rounded-md px-2 py-0.5 max-sm:min-h-9 text-red-400 hover:bg-neutral-800"
                        onClick={() => void revoke(link.id)}
                      >
                        吊销
                      </button>
                    </span>
                  </div>
                );
              })}
            </div>
          )}
          {copyHint && <p className="mt-2 text-xs text-neutral-400">{copyHint}</p>}
        </div>
      )}
    </div>
  );
}

// 预览链接创建入口 (会话/终端分享菜单): 为当前终端标签页创建匿名可开的预览链接。
// 默认只读: 观看者看到当前屏幕与后续实时输出, 不能输入; WEB 端可勾选「允许控制」
// 让观看者获得输入权限 (write 随创建请求发出)。链接有过期与吊销。
// 与主机分享 (host_share 登录账号间分享) 和设备公开链接是不同入口。token 只在
// 创建成功当场持有用于拼公开 URL, 不写日志、不持久化、不进 web storage。
// 数据源按 TRANSPORT 分流: WEB 直连 previewApi (服务端托管 tab 同 id); 桌面走
// 远程中继 — 创建经 preview_share_create (建远程壳+链接+起输出中继), 列表按
// 当前 tab 活跃中继的壳 id 过滤 remoteSharingApi.previewLinks, 吊销同远程。
// 桌面中继创建的预览第一期暂不支持控制, 勾选框禁用并常驻只读。

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
import { IconGlobe } from "../../ui/icons";

// 有效期选项全部落在服务端 minShareTTL(1 分钟)-maxShareTTL(30 天) 边界内;
// 默认 1 小时与服务端 defaultLinkTTL 对齐, 创建时总是显式带 ttl_ms。
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

// 进行中的链接可能带 permission 字段 (read/read_write); 缺失或未知值一律按只读展示。
function previewPermission(link: PreviewLinkView): "read" | "read_write" {
  const raw = (link as PreviewLinkView & { permission?: unknown }).permission;
  return raw === "read_write" ? "read_write" : "read";
}

export function PreviewShareMenu({ tabId, sessionName }: { tabId: string; sessionName?: string }) {
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
        // 桌面: 链接挂在远程壳上, 先查当前 tab 活跃中继的壳 id 再过滤。
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
      // 列表失败不打断创建入口; 下一次打开面板会重试。
    }
  }, [tabId]);

  useEffect(() => {
    if (open) void refresh();
  }, [open, refresh]);

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
        // 桌面中继创建的预览第一期暂不支持控制, 创建签名保持 (tabId, ttlMs)。
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
        className="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
        onClick={toggle}
      >
        <IconGlobe className="h-3.5 w-3.5" />
        预览链接
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-80 rounded-md border border-neutral-700 bg-neutral-900 p-3 shadow-xl">
          <p className="text-xs text-neutral-300">
            为「{sessionName || "当前终端"}」创建预览链接：获得链接的人无需登录即可查看当前屏幕和后续输出。默认只读，观看者不能输入。
          </p>
          <div className="mt-2 flex items-center gap-2">
            <select
              className="rounded-md border border-neutral-700 bg-neutral-800 px-2 py-1 text-xs text-neutral-200"
              value={ttlMs}
              onChange={(event) => setTtlMs(Number(event.target.value))}
            >
              {PREVIEW_TTL_OPTIONS.map((option) => (
                <option key={option.ms} value={option.ms}>
                  {option.label}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="rounded-md bg-indigo-600 px-3 py-1 text-xs text-white hover:bg-indigo-500 disabled:opacity-50"
              disabled={creating}
              onClick={() => void create()}
            >
              {creating ? "创建中…" : "创建链接"}
            </button>
          </div>
          <div className="mt-2 flex items-center gap-2">
            <label className="flex items-center gap-1.5 text-xs text-neutral-300">
              <input
                type="checkbox"
                checked={allowWrite}
                disabled={!WEB}
                onChange={(event) => setAllowWrite(event.target.checked)}
              />
              允许控制
            </label>
            {!WEB && <span className="text-xs text-neutral-500">桌面中继创建的预览第一期暂不支持控制</span>}
          </div>
          {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
          {createdUrl && (
            <div className="mt-2 rounded-md border border-emerald-900/60 bg-emerald-950/40 p-2">
              <p className="break-all text-xs text-emerald-300">{createdUrl}</p>
              <button
                type="button"
                className="mt-1 rounded-md bg-neutral-800 px-2 py-0.5 text-xs text-neutral-200 hover:bg-neutral-700"
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
                  <div key={link.id} className="mt-1 flex items-center justify-between text-xs">
                    <span className="text-neutral-400">有效期至 {new Date(link.expires_at).toLocaleString()}</span>
                    <span className="flex items-center gap-2">
                      <span
                        className={
                          permission === "read_write"
                            ? "rounded-full bg-emerald-900/60 px-2 py-0.5 text-emerald-300"
                            : "rounded-full bg-neutral-800 px-2 py-0.5 text-neutral-400"
                        }
                      >
                        {permission === "read_write" ? "可控制" : "只读"}
                      </span>
                      <button
                        type="button"
                        className="rounded-md px-2 py-0.5 text-red-400 hover:bg-neutral-800"
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

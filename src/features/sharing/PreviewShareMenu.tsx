// 只读预览创建入口 (会话/终端分享菜单): 为当前终端标签页创建匿名可开的只读
// 只读预览链接。观看者看到当前屏幕与后续实时输出, 不能输入; 链接有过期与吊销。
// 与主机分享 (host_share 登录账号间分享) 和设备公开链接是不同入口。token 只在
// 创建成功当场持有用于拼公开 URL, 不写日志、不持久化、不进 web storage。

import { useCallback, useEffect, useRef, useState } from "react";
import {
  createPreviewLink,
  listPreviewLinks,
  previewPublicUrl,
  revokePreviewLink,
  type PreviewLinkView,
} from "./previewApi";
import { describeError } from "../../ui/errorText";
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

export function PreviewShareMenu({ tabId, sessionName }: { tabId: string; sessionName?: string }) {
  const [open, setOpen] = useState(false);
  const [ttlMs, setTtlMs] = useState(PREVIEW_TTL_OPTIONS[1].ms);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [createdUrl, setCreatedUrl] = useState<string | null>(null);
  const [copyHint, setCopyHint] = useState<string | null>(null);
  const [links, setLinks] = useState<PreviewLinkView[]>([]);
  const epochRef = useRef(0);
  const unmountedRef = useRef(false);
  const copyHintTimerRef = useRef<number | null>(null);

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
      const result = await listPreviewLinks();
      if (epoch !== epochRef.current || unmountedRef.current) return;
      setLinks(activePreviews(result.links ?? [], tabId));
    } catch {
      // 列表失败不打断创建入口; 下一次打开面板会重试。
    }
  }, [tabId]);

  useEffect(() => {
    if (open) void refresh();
  }, [open, refresh]);

  const toggle = useCallback(() => {
    if (open) {
      epochRef.current += 1;
      setOpen(false);
      setCreating(false);
      setCreatedUrl(null);
      setError(null);
      return;
    }
    setOpen(true);
  }, [open]);

  const create = useCallback(async () => {
    if (creating) return;
    const epoch = epochRef.current;
    setCreating(true);
    setError(null);
    try {
      const link = await createPreviewLink({ sessionId: tabId, ttlMs });
      if (epoch !== epochRef.current || unmountedRef.current) return;
      setCreatedUrl(previewPublicUrl(link.token));
      void refresh();
    } catch (e) {
      if (epoch !== epochRef.current || unmountedRef.current) return;
      setError(describeError(e));
    } finally {
      if (epoch === epochRef.current && !unmountedRef.current) setCreating(false);
    }
  }, [creating, tabId, ttlMs, refresh]);

  const revoke = useCallback(
    async (id: string) => {
      try {
        await revokePreviewLink(id);
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
      await navigator.clipboard.writeText(url);
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
    <div className="relative inline-block">
      <button
        type="button"
        title="创建只读预览链接"
        className="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
        onClick={toggle}
      >
        <IconGlobe className="h-3.5 w-3.5" />
        只读预览
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-80 rounded-md border border-neutral-700 bg-neutral-900 p-3 shadow-xl">
          <p className="text-xs text-neutral-300">
            为「{sessionName || "当前终端"}」创建只读预览链接：获得链接的人无需登录即可查看当前屏幕和后续输出，不能输入。
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
              {links.map((link) => (
                <div key={link.id} className="mt-1 flex items-center justify-between text-xs">
                  <span className="text-neutral-400">有效期至 {new Date(link.expires_at).toLocaleString()}</span>
                  <button
                    type="button"
                    className="rounded-md px-2 py-0.5 text-red-400 hover:bg-neutral-800"
                    onClick={() => void revoke(link.id)}
                  >
                    吊销
                  </button>
                </div>
              ))}
            </div>
          )}
          {copyHint && <p className="mt-2 text-xs text-neutral-400">{copyHint}</p>}
        </div>
      )}
    </div>
  );
}

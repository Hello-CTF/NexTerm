import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { assetApi, fsApi, toAppError } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import "../../ui/skeleton.css";
import { fileVisual, formatSize } from "./fileTypes";
import { bytesFromBase64 } from "./fileCodec";
import {
  IconAlert,
  IconChevronLeft,
  IconChevronRight,
  IconClose,
  IconEye,
  IconRefresh,
  IconSearch,
} from "../../ui/icons";

const CHUNK_SIZE = 256 * 1024;

const LOG_ENCODINGS = ["utf-8", "gbk", "gb18030", "big5", "latin1"];

const LOG_ENCODING_LABEL: Record<string, string> = {
  "utf-8": "UTF-8",
  gbk: "GBK",
  gb18030: "GB18030",
  big5: "Big5",
  latin1: "Latin1",
};

interface LogChunk {
  offset: number;
  size: number;
  bytes: number;
  truncated: boolean;
  text: string;
}

export interface LogViewerProps {
  sessionId: string;
  path: string;
  onClose?: () => void;
}

function highlight(line: string, query: string): ReactNode {
  const q = query.trim().toLowerCase();
  if (!q) return line;
  const parts: ReactNode[] = [];
  const lower = line.toLowerCase();
  let pos = 0;
  let key = 0;
  for (;;) {
    const hit = lower.indexOf(q, pos);
    if (hit < 0) {
      parts.push(line.slice(pos));
      break;
    }
    if (hit > pos) parts.push(line.slice(pos, hit));
    parts.push(
      <mark key={key++} className="rounded-[2px] bg-amber-300/40 px-0 text-inherit">
        {line.slice(hit, hit + q.length)}
      </mark>,
    );
    pos = hit + q.length;
  }
  return parts;
}

export function LogViewer({ sessionId, path, onClose }: LogViewerProps) {
  const [chunk, setChunk] = useState<LogChunk | null>(null);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<{ message: string; unsupported: boolean } | null>(null);
  const [query, setQuery] = useState("");
  const [pageDraft, setPageDraft] = useState("");
  const [enc, setEnc] = useState("utf-8");
  const encRef = useRef(enc);
  encRef.current = enc;
  const encTouchedRef = useRef(false);
  const offsetRef = useRef(offset);
  offsetRef.current = offset;
  const assetId = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.assetId ?? null);

  const load = useCallback(
    async (requested: number) => {
      setLoading(true);
      setError(null);
      try {
        let res = await fsApi.readRange(sessionId, path, requested, CHUNK_SIZE);
        if (res.size > 0 && res.offset >= res.size) {
          const last = Math.max(0, Math.floor((res.size - 1) / CHUNK_SIZE) * CHUNK_SIZE);
          res = await fsApi.readRange(sessionId, path, last, CHUNK_SIZE);
        }
        const bytes = bytesFromBase64(res.contentBase64);
        setChunk({
          offset: res.offset,
          size: res.size,
          bytes: bytes.length,
          truncated: res.truncated,
          text: new TextDecoder(encRef.current).decode(bytes),
        });
        setOffset(res.offset);
      } catch (e) {
        const app = toAppError(e);
        setChunk(null);
        setError({ message: app.message, unsupported: app.code === "unsupported" });
      } finally {
        setLoading(false);
      }
    },
    [sessionId, path],
  );

  useEffect(() => {
    void load(0);
  }, [load]);

  useEffect(() => {
    if (!assetId) return;
    let cancelled = false;
    void assetApi
      .list()
      .then((assets) => {
        if (cancelled) return;
        const raw = assets.find((a) => a.id === assetId)?.options?.encoding;
        if (typeof raw !== "string") return;
        const value = raw.trim().toLowerCase();
        const normalized = value === "utf8" ? "utf-8" : value === "iso-8859-1" ? "latin1" : value;
        if (!LOG_ENCODINGS.includes(normalized) || encTouchedRef.current) return;
        encRef.current = normalized;
        setEnc(normalized);
        void load(offsetRef.current);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [assetId, load]);

  const pages = chunk ? Math.max(1, Math.ceil(chunk.size / CHUNK_SIZE)) : 1;
  const page = Math.floor(offset / CHUNK_SIZE) + 1;

  useEffect(() => {
    setPageDraft(String(page));
  }, [page]);

  const gotoPage = (next: number) => {
    const clamped = Math.min(pages, Math.max(1, next));
    void load((clamped - 1) * CHUNK_SIZE);
  };

  const changeEncoding = (next: string) => {
    encTouchedRef.current = true;
    encRef.current = next;
    setEnc(next);
    void load(offsetRef.current);
  };

  const lines = useMemo(() => chunk?.text.split("\n") ?? [], [chunk]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return null;
    return lines
      .map((text, index) => ({ text, index }))
      .filter(({ text }) => text.toLowerCase().includes(q));
  }, [lines, query]);

  const { Icon, tone } = fileVisual(path, "file");

  return (
    <div className="nx-pane bg-term">
      <div className="nx-toolbar">
        <Icon size={14} className={`shrink-0 ${tone}`} />
        <span className="nx-toolbar-title truncate font-mono">{path}</span>
        <span className="nx-badge nx-badge-amber shrink-0" title="只读视图，不会修改远端文件">
          <span className="nx-dot" />
          只读日志
        </span>
        <label
          className="nx-btn nx-btn-ghost nx-btn-sm relative shrink-0 font-mono max-[560px]:hidden"
          title={`日志编码：${LOG_ENCODING_LABEL[enc]}（默认跟随资产的终端编码设置）`}
        >
          {LOG_ENCODING_LABEL[enc]}
          <select
            className="absolute h-0 w-0 opacity-0"
            value={enc}
            aria-label="日志编码"
            onChange={(e) => changeEncoding(e.target.value)}
          >
            {LOG_ENCODINGS.map((k) => (
              <option key={k} value={k}>
                {LOG_ENCODING_LABEL[k]}
              </option>
            ))}
          </select>
        </label>
        <div className="nx-field w-44 max-w-none shrink-0 max-[560px]:hidden">
          <span className="nx-field-icon">
            <IconSearch size={12} />
          </span>
          <input
            className="nx-input nx-input-sm font-mono"
            value={query}
            spellCheck={false}
            placeholder="过滤当前分块…"
            title="在已加载的分块内过滤行；翻页后需重新输入"
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        {filtered && (
          <span className="shrink-0 text-[11px] text-neutral-500" role="status">
            {filtered.length} 行匹配
          </span>
        )}
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn"
          title="重新读取当前分块（日志文件可能已增长）"
          disabled={loading}
          onClick={() => void load(offset)}
        >
          <IconRefresh size={13} />
        </button>
        <span className="nx-divider-v" />
        <div className="flex shrink-0 items-center gap-0.5" role="group" aria-label="分页">
          <button
            className="nx-btn nx-btn-ghost nx-btn-sm"
            disabled={loading || page <= 1}
            onClick={() => gotoPage(1)}
            title="第一页"
          >
            首页
          </button>
          <button
            className="nx-icon-btn"
            disabled={loading || page <= 1}
            onClick={() => gotoPage(page - 1)}
            title="上一页"
            aria-label="上一页"
          >
            <IconChevronLeft size={13} />
          </button>
          <form
            className="flex items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault();
              const next = Number.parseInt(pageDraft, 10);
              if (Number.isFinite(next)) gotoPage(next);
              else setPageDraft(String(page));
            }}
          >
            <input
              className="nx-input nx-input-sm w-14 text-center font-mono"
              value={pageDraft}
              aria-label="页码"
              title="输入页码后回车跳转"
              onChange={(e) => setPageDraft(e.target.value)}
              onFocus={(e) => e.currentTarget.select()}
            />
          </form>
          <span className="shrink-0 text-[11px] text-neutral-500">/ {pages} 页</span>
          <button
            className="nx-icon-btn"
            disabled={loading || page >= pages}
            onClick={() => gotoPage(page + 1)}
            title="下一页"
            aria-label="下一页"
          >
            <IconChevronRight size={13} />
          </button>
          <button
            className="nx-btn nx-btn-ghost nx-btn-sm"
            disabled={loading || page >= pages}
            onClick={() => gotoPage(pages)}
            title="最后一页（日志末尾）"
          >
            末页
          </button>
        </div>
        {onClose && (
          <button className="nx-icon-btn" onClick={onClose} title="关闭日志查看器">
            <IconClose size={14} />
          </button>
        )}
      </div>

      {error ? (
        <div className="m-3 flex items-start gap-2 nx-alert nx-alert-danger">
          <IconAlert size={14} className="mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1">
            {error.unsupported ? (
              <div className="break-words">
                当前连接的后端不支持远程日志查看：日志查看器需要 SSH/SFTP 的分块读取能力，
                WinRM 与本地后端暂不支持。可以改用内置编辑器打开，或下载到本机查看。
              </div>
            ) : (
              <>
                <div className="break-words">{describeError(error.message)}</div>
                <button className="nx-link mt-1 text-[11px]" onClick={() => void load(offset)}>
                  重试
                </button>
              </>
            )}
          </div>
        </div>
      ) : loading && !chunk ? (
        <div role="status" aria-label="加载中" className="p-3">
          {Array.from({ length: 10 }, (_, i) => (
            <div key={i} className="nx-skeleton-row" aria-hidden="true">
              <span className="nx-skeleton nx-skeleton-icon" />
              <span className="nx-skeleton min-w-0 flex-1" />
            </div>
          ))}
          <span className="nx-sr-only">加载中…</span>
        </div>
      ) : chunk && chunk.size === 0 ? (
        <div className="nx-empty">这个文件是空的</div>
      ) : chunk ? (
        <div
          role="log"
          aria-label={`日志内容 ${path}`}
          className="min-h-0 flex-1 overflow-auto px-1 py-1 font-mono text-[12px] leading-[1.55]"
        >
          {(filtered ?? lines.map((text, index) => ({ text, index }))).map(({ text, index }) => (
            <div key={index} className="flex gap-3 rounded px-2 hover:bg-neutral-800/30">
              <span className="w-12 shrink-0 select-none text-right text-neutral-600">
                {index + 1}
              </span>
              <span className="whitespace-pre">{highlight(text, query)}</span>
            </div>
          ))}
          {filtered && filtered.length === 0 && (
            <div className="nx-hint px-2 py-4 text-center">当前分块没有匹配的行</div>
          )}
        </div>
      ) : null}

      {chunk && !error && (
        <div className="flex h-[24px] shrink-0 items-center gap-2 border-t border-neutral-800/60 px-3 text-[10.5px] text-neutral-500">
          <IconEye size={11} />
          <span>只读，不修改远端文件</span>
          <span className="text-neutral-700">|</span>
          <span>
            字节 {chunk.offset.toLocaleString()}–
            {(chunk.offset + Math.max(chunk.bytes - 1, 0)).toLocaleString()} /{" "}
            {formatSize(chunk.size)}
          </span>
          <span className="text-neutral-700">|</span>
          <span>
            分块 {page} / {pages}（每页 {formatSize(CHUNK_SIZE)}，行号为分块内序号
            {chunk.offset > 0 ? "，首行可能从半行开始" : ""}
            {chunk.truncated ? "，末行延续到下一页" : ""}）
          </span>
          <div className="nx-spacer" />
          <span className="max-[560px]:hidden">搜索仅过滤当前已加载分块</span>
        </div>
      )}
    </div>
  );
}

import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { syncApi } from "../../ipc/commands";
import type { ImportReport, SyncBundle, SyncDigest } from "../../ipc/types";
import { pickBundleFile, saveBundleFile } from "../../ipc/bundleFiles";
import { baseName } from "../../ipc/webFiles";
import { openWailsFile, saveWailsFile } from "../../ipc/wails";
import { useUi } from "../../app/store";
import { DEMO, WEB } from "../../demo";
import { promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ImportReportView } from "./SyncCardReport";
import {
  IconArchive,
  IconCheckCircle,
  IconChevronDown,
  IconChevronRight,
  IconDownload,
  IconInfo,
  IconLock,
  IconRefresh,
  IconShield,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

const BUNDLE_PROTOCOL = 1;

const canEncryptBundle = !WEB && !DEMO;

type ExportFormat = "" | "encrypted" | "plaintext";

function isEncryptedBundleError(e: unknown): boolean {
  const o = e as { code?: unknown; message?: unknown } | null;
  return o?.code === "bad_param" && typeof o.message === "string" && o.message.includes("已加密");
}

function nxbmContainerKind(text: string): "encrypted" | "plaintext" | null {
  if (!text.startsWith("NXBM")) return null;
  return text.charCodeAt(5) & 1 ? "encrypted" : "plaintext";
}

interface BundlePreview {
  bundle: SyncBundle;
  fileName: string;
  existingAssetIds: Set<string>;
  danglingCredAssets: number;
  tombstones: number;
}

function parseBundle(text: string): { bundle?: SyncBundle; error?: string } {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { error: "不是合法的 JSON 文件" };
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { error: "资产包必须是一个 JSON 对象" };
  }
  const candidate = raw as Record<string, unknown>;
  if (candidate.protocol !== BUNDLE_PROTOCOL) {
    return {
      error: `不支持的资产包协议版本（文件为 ${String(candidate.protocol)}，当前支持 ${BUNDLE_PROTOCOL}）`,
    };
  }
  for (const key of ["groups", "assets", "creds"] as const) {
    if (candidate[key] !== undefined && !Array.isArray(candidate[key])) {
      return { error: `资产包字段 ${key} 必须是数组` };
    }
  }
  const bundle = candidate as unknown as SyncBundle;
  bundle.groups ??= [];
  bundle.assets ??= [];
  bundle.creds ??= [];
  return { bundle };
}

function formatExportedAt(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "未知";
  return new Date(ms).toLocaleString();
}

async function pickEncryptedBundle(): Promise<{ name: string; text: string } | null> {
  const password = await promptText("该资产包已加密。请输入口令，然后重新选择该文件：", "", {
    secret: true,
  });
  if (!password) return null;
  const path = await openWailsFile([
    { name: "NexTerm 加密资产包", extensions: ["nxbm"] },
    { name: "所有文件", extensions: ["*"] },
  ]);
  if (!path) return null;
  const text = await syncApi.readBundleFile(path, password);
  return { name: baseName(path), text };
}

export function SyncBundleCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();

  const [digest, setDigest] = useState<SyncDigest | null>(null);
  const [digestError, setDigestError] = useState<string | null>(null);

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [withCreds, setWithCreds] = useState(false);
  const [exportFormat, setExportFormat] = useState<ExportFormat>("");
  const [password, setPassword] = useState("");
  const [password2, setPassword2] = useState("");
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);
  const [exportSummary, setExportSummary] = useState<{
    assets: number;
    groups: number;
    creds: number;
    encrypted: boolean;
    warnings: string[];
  } | null>(null);

  const [preview, setPreview] = useState<BundlePreview | null>(null);
  const [importError, setImportError] = useState<string | null>(null);
  const [force, setForce] = useState(false);
  const [importing, setImporting] = useState(false);
  const [report, setReport] = useState<ImportReport | null>(null);

  const exportPanelId = useId();
  const importPanelId = useId();
  const [exportOpen, setExportOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);

  const refreshDigest = useCallback(() => {
    setDigestError(null);
    return syncApi
      .digest()
      .then(setDigest)
      .catch((e: unknown) => setDigestError(describeError(e)));
  }, []);

  useEffect(() => {
    void refreshDigest();
  }, [refreshDigest]);

  const exportable = useMemo(
    () => (digest?.assets ?? []).filter((a) => a.deletedAt === null),
    [digest],
  );

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const doExport = async () => {
    const ids = [...selected];
    if (ids.length === 0) {
      pushToast("error", "先勾选要导出的资产");
      return;
    }
    if (!exportFormat) return;
    const usePassword = exportFormat === "encrypted";
    if (usePassword && password !== password2) {
      setExportError("两次输入的导出口令不一致");
      return;
    }
    setExporting(true);
    setExportError(null);
    setExportSummary(null);
    try {
      const bundle = await syncApi.exportAssets(ids, withCreds);
      const text = JSON.stringify(bundle, null, 2);
      const stamp = new Date().toISOString().slice(0, 10);
      if (usePassword) {
        const path = await saveWailsFile(`nexterm-assets-${stamp}.nxbm`);
        if (!path) return;
        await syncApi.writeBundleFile(path, text, password);
        setPassword("");
        setPassword2("");
      } else {
        const saved = await saveBundleFile(`nexterm-assets-${stamp}.json`, text);
        if (!saved) return;
      }
      setExportSummary({
        assets: bundle.assets.length,
        groups: bundle.groups.length,
        creds: bundle.creds.length,
        encrypted: usePassword,
        warnings: bundle.warnings ?? [],
      });
      pushToast(
        "success",
        usePassword
          ? `已加密导出 ${bundle.assets.length} 条资产（.nxbm 容器）`
          : `已导出 ${bundle.assets.length} 条资产${bundle.creds.length > 0 ? `（含 ${bundle.creds.length} 条凭据）` : ""}`,
      );
    } catch (e) {
      setExportError(describeError(e));
    } finally {
      setExporting(false);
    }
  };

  const doPick = async () => {
    setImportError(null);
    setPreview(null);
    setReport(null);
    try {
      let picked: { name: string; text: string } | null = null;
      try {
        picked = await pickBundleFile();
      } catch (e) {
        if (!isEncryptedBundleError(e) || !canEncryptBundle) throw e;
        picked = await pickEncryptedBundle();
      }
      if (!picked) return;
      const containerKind = nxbmContainerKind(picked.text);
      if (containerKind === "encrypted") {
        setImportError(`${picked.name}：这是加密的资产包，请在桌面版 NexTerm 中导入（当前环境无法解密）`);
        return;
      }
      if (containerKind === "plaintext") {
        setImportError(`${picked.name}：这是 NexTerm 资产包容器（.nxbm），请在桌面版 NexTerm 中导入（当前环境无法读取）`);
        return;
      }
      const { bundle, error } = parseBundle(picked.text);
      if (!bundle) {
        setImportError(`${picked.name}：${error}`);
        return;
      }
      const existing = new Set((digest?.assets ?? []).map((a) => a.id));
      const credIds = new Set(bundle.creds.map((c) => c.id));
      setPreview({
        bundle,
        fileName: picked.name,
        existingAssetIds: existing,
        danglingCredAssets: bundle.assets.filter((a) => a.credId && !credIds.has(a.credId)).length,
        tombstones: bundle.assets.filter((a) => a.deletedAt !== null).length,
      });
    } catch (e) {
      setImportError(describeError(e));
    }
  };

  const doImport = async () => {
    if (!preview) return;
    setImporting(true);
    setImportError(null);
    setReport(null);
    try {
      const data = await syncApi.importBundle(preview.bundle, force);
      setReport(data);
      setPreview(null);
      setSelected(new Set());
      void qc.invalidateQueries();
      await refreshDigest();
      const touched =
        data.assetsCreated +
        data.assetsUpdated +
        data.groupsCreated +
        data.groupsUpdated +
        data.credsCreated +
        data.credsUpdated;
      pushToast(
        touched > 0 ? "success" : "error",
        touched > 0
          ? `导入完成：新建 ${data.assetsCreated + data.groupsCreated + data.credsCreated} · 更新 ${
              data.assetsUpdated + data.groupsUpdated + data.credsUpdated
            }`
          : "没有任何条目被写入，请看下方原因",
      );
    } catch (e) {
      setImportError(describeError(e));
    } finally {
      setImporting(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconArchive size={15} className="text-neutral-400" />
        <span className="nx-card-title">资产包（文件导入 / 导出）</span>
      </div>
      <p className="nx-hint mb-3.5">
        把资产打包成一个 JSON 文件带走，或从文件恢复到本机。资产包走文件，不经过任何服务器，
        与上方「账号同步」的推送 / 拉取互不影响。
      </p>

      {digestError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">本机资产摘要读取失败 · {digestError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void refreshDigest()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
          aria-expanded={exportOpen}
          aria-controls={exportPanelId}
          onClick={() => setExportOpen((v) => !v)}
        >
          {exportOpen ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          导出资产包
        </button>
        <button
          type="button"
          className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
          aria-expanded={importOpen}
          aria-controls={importPanelId}
          onClick={() => setImportOpen((v) => !v)}
        >
          {importOpen ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          导入资产包
        </button>
      </div>

      {exportOpen && (
      <div id={exportPanelId} className="mt-2 flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">导出</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setSelected(new Set(exportable.map((a) => a.id)))}>
            全选
          </button>
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setSelected(new Set())}>
            清空
          </button>
        </div>

        {exportable.length === 0 ? (
          <div className="nx-hint py-2 text-[12px]">本机还没有可导出的资产。</div>
        ) : (
          <div className="max-h-[220px] overflow-y-auto rounded border border-neutral-800/60">
            {exportable.map((a) => (
              <label
                key={a.id}
                className="flex cursor-pointer items-center gap-2 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0 hover:bg-neutral-800/30"
              >
                <input
                  type="checkbox"
                  className="h-3.5 w-3.5 shrink-0"
                  checked={selected.has(a.id)}
                  onChange={() => toggle(a.id)}
                />
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={a.name}>
                  {a.name}
                </span>
                <span className="min-w-0 shrink truncate font-mono text-[11px] text-neutral-500" title={a.host ?? a.kind}>
                  {a.host ?? a.kind}
                </span>
                {a.hasCred ? (
                  <span className="nx-hint shrink-0" title="关联了凭据（密码/私钥）">
                    带凭据
                  </span>
                ) : null}
              </label>
            ))}
          </div>
        )}

        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5 h-4 w-4 shrink-0"
            checked={withCreds}
            onChange={(e) => setWithCreds(e.target.checked)}
          />
          <span className="text-[12px] text-neutral-300">
            导出凭据（密码 / 私钥会写进文件）
            <span className="nx-hint block">
              不勾时，资产里只保留凭据引用，拿到文件的人看不到任何秘密，但导入后需要重新关联凭据。
            </span>
          </span>
        </label>

        {withCreds && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconShield size={14} className="mt-0.5 shrink-0" />
            <div>
              <b>这个文件将包含可还原的凭据明文</b>：任何拿到这个文件的人都能登上对应的服务器。
              请只在可信设备之间传递，用毕及时删除；私钥文件会一并内联进资产包。
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="bundle-format">
            导出格式
          </label>
          <select
            id="bundle-format"
            className="nx-input min-w-0 flex-1 min-[560px]:w-[280px] min-[560px]:flex-none"
            value={exportFormat}
            onChange={(e) => setExportFormat(e.target.value as ExportFormat)}
          >
            <option value="">选择导出格式…</option>
            {canEncryptBundle ? (
              <option value="encrypted">加密资产包（.nxbm · 推荐）</option>
            ) : (
              <option value="encrypted" disabled>
                加密资产包（.nxbm · 仅桌面版）
              </option>
            )}
            <option value="plaintext">明文 JSON（拿到文件即可直接读取）</option>
          </select>
        </div>

        {!canEncryptBundle && (
          <p className="nx-hint text-[12px]">
            加密导出（.nxbm）仅在桌面版 NexTerm 可用；当前环境由浏览器直接保存明文 JSON。
          </p>
        )}

        {exportFormat === "encrypted" && (
          <div className="flex flex-col gap-2">
            <p className="nx-hint text-[12px]">
              用口令把资产包加密成 <code>.nxbm</code> 容器，只有知道口令的人才能导入。
              口令无法找回，丢失后将无法导入，请妥善保管。
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="bundle-password">
                导出口令
              </label>
              <input
                id="bundle-password"
                type="password"
                className="nx-input min-w-0 flex-1 font-mono"
                autoComplete="off"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="bundle-password2">
                确认口令
              </label>
              <input
                id="bundle-password2"
                type="password"
                className="nx-input min-w-0 flex-1 font-mono"
                autoComplete="off"
                value={password2}
                onChange={(e) => setPassword2(e.target.value)}
              />
            </div>
            {password !== password2 && (
              <span className="text-[12px] text-amber-300">两次输入的导出口令不一致</span>
            )}
          </div>
        )}

        {exportFormat === "plaintext" && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconShield size={14} className="mt-0.5 shrink-0" />
            <div>
              <b>将以明文导出</b>：任何拿到这个文件的人都能直接读取其中的资产
              （主机、账号{withCreds ? "与可还原的凭据明文" : ""}）。
              请只在可信设备之间传递，用毕及时删除。
            </div>
          </div>
        )}

        <div>
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={
              exporting ||
              selected.size === 0 ||
              !exportFormat ||
              (exportFormat === "encrypted" && (password.length === 0 || password !== password2))
            }
            title={!exportFormat ? "先选择导出格式" : undefined}
            onClick={() => void doExport()}
          >
            {exporting ? <IconRefresh size={12} className="animate-spin" /> : <IconDownload size={12} />}
            {exporting
              ? "导出中…"
              : exportFormat === "encrypted"
                ? "导出为加密资产包 (.nxbm)"
                : exportFormat === "plaintext"
                  ? "导出为明文 JSON 文件"
                  : "导出为 JSON 文件"}
          </button>
        </div>

        {exportError && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <span className="min-w-0 flex-1 break-words">{exportError}</span>
          </div>
        )}

        {exportSummary && (
          <div className="nx-alert flex items-start gap-2">
            <IconCheckCircle size={13} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div>
                {exportSummary.encrypted ? "已加密导出" : "导出完成"}：资产 {exportSummary.assets} · 分组{" "}
                {exportSummary.groups} · 凭据 {exportSummary.creds}
              </div>
              {exportSummary.warnings.length > 0 && (
                <ul className="mt-1 list-disc pl-4 text-[11.5px] text-amber-200">
                  {exportSummary.warnings.map((w, i) => (
                    <li key={i}>{w}</li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}
      </div>
      )}

      {importOpen && (
      <div id={importPanelId} className="mt-2 flex flex-col gap-2">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">导入</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-outline nx-btn-sm" disabled={importing} onClick={() => void doPick()}>
            <IconUpload size={12} />
            选择资产包文件…
          </button>
        </div>
        <p className="nx-hint mb-2 text-[12px]">
          {canEncryptBundle
            ? "支持旧版 .json 资产包与 .nxbm 加密资产包（加密包导入时需输入口令）。"
            : "支持旧版 .json 资产包；.nxbm 加密资产包请在桌面版 NexTerm 中导入。"}
        </p>

        {importError && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <span className="min-w-0 flex-1 break-words">{importError}</span>
          </div>
        )}

        {preview && (
          <div className="mt-2 flex flex-col gap-2">
            <div className="rounded border border-neutral-800/60 px-2.5 py-2 text-[12px] text-neutral-300">
              <div className="mb-1 font-semibold text-neutral-200">{preview.fileName}</div>
              <div className="nx-hint">
                来源 {preview.bundle.origin || "未知"} · 导出于 {formatExportedAt(preview.bundle.exportedAt)}
              </div>
              <div className="mt-1">
                资产 {preview.bundle.assets.length}（其中 {preview.existingAssetIds.size > 0 ? preview.bundle.assets.filter((a) => preview.existingAssetIds.has(a.id)).length : 0} 条本机已存在）
                · 分组 {preview.bundle.groups.length} · 凭据 {preview.bundle.creds.length}
              </div>
            </div>

            {preview.bundle.creds.length > 0 && (
              <div className="nx-alert nx-alert-danger flex items-start gap-2">
                <IconLock size={14} className="mt-0.5 shrink-0" />
                <div>
                  <b>包含 {preview.bundle.creds.length} 条凭据</b>（
                  {preview.bundle.creds.map((c) => c.name).join("、")}
                  ），导入后会写入本机凭据库；凭据库需要处于解锁状态。本机同 ID 的凭据会被覆盖。
                </div>
              </div>
            )}

            {preview.danglingCredAssets > 0 && (
              <div className="nx-alert nx-alert-info flex items-start gap-2">
                <IconInfo size={14} className="mt-0.5 shrink-0" />
                <div>
                  {preview.danglingCredAssets} 条资产引用的凭据不在包内，导入后这些引用会被清除，需要重新关联凭据。
                </div>
              </div>
            )}

            {preview.tombstones > 0 && (
              <div className="nx-alert nx-alert-info flex items-start gap-2">
                <IconInfo size={14} className="mt-0.5 shrink-0" />
                <div>
                  包含 {preview.tombstones} 条删除标记，导入后本机对应资产会被一并标记删除。
                </div>
              </div>
            )}

            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                checked={force}
                onChange={(e) => setForce(e.target.checked)}
              />
              <span className="text-[12px] text-neutral-300">
                强制覆盖较新的本机条目
                <span className="nx-hint block">
                  不勾时，本机版本较新的资产会被跳过，不会被包里的旧版本覆盖。
                </span>
              </span>
            </label>

            <div>
              <button
                className="nx-btn nx-btn-primary nx-btn-sm"
                disabled={importing}
                onClick={() => void doImport()}
              >
                {importing ? <IconRefresh size={12} className="animate-spin" /> : <IconUpload size={12} />}
                {importing ? "导入中…" : `确认导入 ${preview.bundle.assets.length} 条资产`}
              </button>
            </div>
          </div>
        )}

        {report && <ImportReportView title="导入结果" data={report} />}
      </div>
      )}
    </section>
  );
}

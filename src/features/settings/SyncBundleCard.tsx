import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { syncApi } from "../../ipc/commands";
import type { ImportReport, SyncBundle, SyncDigest } from "../../ipc/types";
import { pickBundleBuffer, saveBundleBytes } from "../../ipc/bundleFiles";
import { buildPlaintextNxbm, parseNxbmContainer } from "../../ipc/nxbm";
import { baseName } from "../../ipc/webFiles";
import { openWailsFile, saveWailsFile } from "../../ipc/wails";
import { useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
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
import { t } from "../../i18n";

const BUNDLE_PROTOCOL = 1;

const canEncryptBundle = !WEB;

function isEncryptedBundleError(e: unknown): boolean {
  const o = e as { code?: unknown; message?: unknown } | null;
  return o?.code === "bad_param" && typeof o.message === "string" && o.message.includes("已加密");
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
    return { error: t("不是合法的 JSON 文件") };
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { error: t("资产包内容格式不正确") };
  }
  const candidate = raw as Record<string, unknown>;
  if (candidate.protocol !== BUNDLE_PROTOCOL) {
    return {
      error: t("不支持的资产包版本（文件为 {value}，当前支持 {BUNDLE_PROTOCOL}）", { value: String(candidate.protocol), BUNDLE_PROTOCOL }),
    };
  }
  for (const key of ["groups", "assets", "creds"] as const) {
    if (candidate[key] !== undefined && !Array.isArray(candidate[key])) {
      return { error: t("资产包中的 {key} 必须是列表", { key }) };
    }
  }
  const bundle = candidate as unknown as SyncBundle;
  bundle.groups ??= [];
  bundle.assets ??= [];
  bundle.creds ??= [];
  return { bundle };
}

function formatExportedAt(ms: number): string | null {
  if (!Number.isFinite(ms) || ms <= 0) return null;
  return new Date(ms).toLocaleString();
}

// BundleProvenance lists only the provenance a bundle actually records, and nothing when it records none.
function BundleProvenance({ origin, exportedAt }: { origin: string | undefined; exportedAt: number }) {
  const exported = formatExportedAt(exportedAt);
  const text = [origin?.trim() ? t("来源 {trim}", { trim: origin.trim() }) : null, exported ? t("导出于 {exported}", { exported }) : null]
    .filter((part): part is string => part !== null)
    .join(" · ");
  return text ? <div className="nx-hint">{text}</div> : null;
}

async function pickEncryptedBundlePayload(path: string): Promise<string | null> {
  const password = await promptText(t("该资产包已加密。请输入口令："), "", { secret: true });
  if (!password) return null;
  return syncApi.readBundleFile(path, password);
}

export function SyncBundleCard() {
  const pushToast = useUi((s) => s.pushToast);
  const qc = useQueryClient();

  const [digest, setDigest] = useState<SyncDigest | null>(null);
  const [digestError, setDigestError] = useState<string | null>(null);

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [withCreds, setWithCreds] = useState(false);
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
  const digestRequestRef = useRef(0);
  const mountedRef = useRef(true);

  const exportPanelId = useId();
  const importPanelId = useId();
  const [exportOpen, setExportOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);

  const refreshDigest = useCallback(() => {
    const requestId = ++digestRequestRef.current;
    setDigestError(null);
    return syncApi
      .digest()
      .then((result) => {
        if (!mountedRef.current || digestRequestRef.current !== requestId) return;
        setDigest(result);
      })
      .catch((e: unknown) => {
        if (!mountedRef.current || digestRequestRef.current !== requestId) return;
        setDigestError(describeError(e));
      });
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void refreshDigest();
    return () => {
      mountedRef.current = false;
    };
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
      pushToast("error", t("请先选择要导出的资产"));
      return;
    }
    if (password !== password2) {
      setExportError(t("两次输入的导出口令不一致"));
      return;
    }
    setExporting(true);
    setExportError(null);
    setExportSummary(null);
    try {
      const bundle = await syncApi.exportAssets(ids, withCreds);
      const text = JSON.stringify(bundle, null, 2);
      const stamp = new Date().toISOString().slice(0, 10);
      const encrypted = canEncryptBundle && password !== "";
      if (canEncryptBundle) {
        const path = await saveWailsFile(`nexterm-assets-${stamp}.nxbm`);
        if (!path) return;
        await syncApi.writeBundleFile(path, text, password);
        setPassword("");
        setPassword2("");
      } else {
        const saved = await saveBundleBytes(`nexterm-assets-${stamp}.nxbm`, buildPlaintextNxbm(text));
        if (!saved) return;
      }
      setExportSummary({
        assets: bundle.assets.length,
        groups: bundle.groups.length,
        creds: bundle.creds.length,
        encrypted,
        warnings: bundle.warnings ?? [],
      });
      pushToast(
        "success",
        encrypted
          ? t("已加密导出 {length} 条资产（.nxbm 容器）", { length: bundle.assets.length })
          : t("已导出 {length} 条资产（.nxbm 容器）", { length: bundle.assets.length }),
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
      let fileName: string;
      let payload: string;
      if (canEncryptBundle) {
        const path = await openWailsFile([{ name: t("NexTerm 资产包"), extensions: ["nxbm"] }]);
        if (!path) return;
        fileName = baseName(path);
        try {
          payload = await syncApi.readBundleFile(path);
        } catch (e) {
          if (!isEncryptedBundleError(e)) throw e;
          const decrypted = await pickEncryptedBundlePayload(path);
          if (!decrypted) return;
          payload = decrypted;
        }
      } else {
        const picked = await pickBundleBuffer();
        if (!picked) return;
        fileName = picked.name;
        const parsed = parseNxbmContainer(picked.buffer);
        if (parsed.kind === "encrypted") {
          setImportError(t("{name}：该资产包已加密，请在桌面版 NexTerm 中导入", { name: picked.name }));
          return;
        }
        if (parsed.kind === "invalid") {
          setImportError(`${picked.name}：${parsed.error}`);
          return;
        }
        payload = parsed.payload;
      }
      const { bundle, error } = parseBundle(payload);
      if (!bundle) {
        setImportError(`${fileName}：${error}`);
        return;
      }
      const existing = new Set((digest?.assets ?? []).map((a) => a.id));
      const credIds = new Set(bundle.creds.map((c) => c.id));
      setPreview({
        bundle,
        fileName,
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
          ? t("导入完成：新建 {value} · 更新 {value2}", { value: data.assetsCreated + data.groupsCreated + data.credsCreated, value2: data.assetsUpdated + data.groupsUpdated + data.credsUpdated })
          : t("未写入任何条目，原因见下方"),
      );
    } catch (e) {
      setImportError(describeError(e));
    } finally {
      setImporting(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconArchive size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("资产包（文件导入与导出）")}</span>
      </div>
      <p className="nx-hint mb-3.5">
        {t("将资产导出为 .nxbm 文件，或从文件导入本机；此功能与上方「账号同步」的推送和拉取互不影响。")}
      </p>

      {digestError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{t("未能读取本机资产摘要：{digestError}。请重试。", { digestError })}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void refreshDigest()}>
            <IconRefresh size={12} />
            {t("重试")}
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
          {t("导出资产包")}
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
          {t("导入资产包")}
        </button>
      </div>

      {exportOpen && (
      <div id={exportPanelId} className="mt-2 flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">{t("导出")}</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setSelected(new Set(exportable.map((a) => a.id)))}>
            {t("全选")}
          </button>
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setSelected(new Set())}>
            {t("清空")}
          </button>
        </div>

        {exportable.length === 0 ? (
          <div className="nx-hint py-2 text-[12px]">{t("本机暂无可导出的资产。")}</div>
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
                  <span className="nx-hint shrink-0" title={t("已关联凭据（密码或私钥）")}>
                    {t("含凭据")}
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
            {t("导出凭据（密码、内联私钥和 API Key 会随资产包写入）")}
            <span className="nx-hint block">
              {t("路径引用仅保存路径，目标环境须能访问同一路径。未勾选时，资产仅保留凭据引用，导入后需重新关联凭据。")}
            </span>
          </span>
        </label>

        {withCreds && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconShield size={14} className="mt-0.5 shrink-0" />
            <div>
              <b>{t("资产包将包含敏感凭据信息")}</b>{t("：未加密导出时，任何获得该文件的人均可读取密码、内联私钥和 API Key；加密导出时，口令泄露也会造成同样风险。请仅在可信设备之间传递，用后及时删除。")}
            </div>
          </div>
        )}

        {canEncryptBundle && (
          <div className="flex flex-col gap-2">
            <p className="nx-hint text-[12px]">
              {t("导出为")} <code>.nxbm</code> {t("容器。口令可选；留空时不加密，任何获得该文件的人均可读取。口令丢失后无法导入，请妥善保管。")}
            </p>
            <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
              <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="bundle-password">
                {t("导出口令")}
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
            <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
              <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="bundle-password2">
                {t("确认口令")}
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
              <span className="text-[12px] text-amber-300">{t("两次输入的导出口令不一致")}</span>
            )}
          </div>
        )}

        {!canEncryptBundle && (
          <p className="nx-hint text-[12px]">
            {t("浏览器导出为未加密的 .nxbm 容器，任何获得该文件的人均可直接读取；口令加密导出仅在桌面版 NexTerm 可用。")}
          </p>
        )}

        {canEncryptBundle && password === "" && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconShield size={14} className="mt-0.5 shrink-0" />
            <div>
              <b>{t("未设置口令")}</b>{t("：任何人均可直接读取导出的 .nxbm 文件，其中包括主机和账号；若同时导出凭据，还包含凭据内容。请仅在可信设备之间传递，用后及时删除。")}
            </div>
          </div>
        )}

        <div>
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={exporting || selected.size === 0 || password !== password2}
            onClick={() => void doExport()}
          >
            {exporting ? <IconRefresh size={12} className="animate-spin" /> : <IconDownload size={12} />}
            {exporting ? t("正在导出…") : t("导出资产包（.nxbm）")}
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
                {t("{value}（.nxbm 容器）：资产 {assets} · 分组{value2}{groups} · 凭据 {creds}", { value: exportSummary.encrypted ? t("已加密导出") : t("已导出"), assets: exportSummary.assets, value2: " ", groups: exportSummary.groups, creds: exportSummary.creds })}
              </div>
              {exportSummary.warnings.length > 0 && (
                <ul className="mt-1 list-disc pl-4 text-[11.5px]">
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
          <span className="w-[76px] shrink-0 text-[12px] text-neutral-400">{t("导入")}</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-outline nx-btn-sm" disabled={importing} onClick={() => void doPick()}>
            <IconUpload size={12} />
            {t("选择资产包文件…")}
          </button>
        </div>
        <p className="nx-hint mb-2 text-[12px]">
          {canEncryptBundle
            ? t("仅支持 .nxbm 资产包；加密资产包需输入口令后导入。")
            : t("仅支持未加密的 .nxbm 资产包；加密资产包请使用桌面版 NexTerm 导入。")}
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
              <BundleProvenance origin={preview.bundle.origin} exportedAt={preview.bundle.exportedAt} />
              <div className="mt-1">
                {t("资产 {length}（其中 {value} 条本机已存在） · 分组 {length2} · 凭据 {length3}", { length: preview.bundle.assets.length, value: preview.existingAssetIds.size > 0 ? preview.bundle.assets.filter((a) => preview.existingAssetIds.has(a.id)).length : 0, length2: preview.bundle.groups.length, length3: preview.bundle.creds.length })}
              </div>
            </div>

            {preview.bundle.creds.length > 0 && (
              <div className="nx-alert nx-alert-danger flex items-start gap-2">
                <IconLock size={14} className="mt-0.5 shrink-0" />
                <div>
                  <b>{t("包含 {length} 条凭据", { length: preview.bundle.creds.length })}</b>{t("（{join}），导入后将写入本机凭据库，凭据库需处于解锁状态；本机同 ID 的凭据将被覆盖。", { join: preview.bundle.creds.map((c) => c.name).join("、") })}
                </div>
              </div>
            )}

            {preview.danglingCredAssets > 0 && (
              <div className="nx-alert nx-alert-info flex items-start gap-2">
                <IconInfo size={14} className="mt-0.5 shrink-0" />
                <div>
                  {t("{danglingCredAssets} 条资产引用的凭据不在包内；若本机也没有同 ID 凭据，导入后引用将被清除，需重新关联凭据。", { danglingCredAssets: preview.danglingCredAssets })}
                </div>
              </div>
            )}

            {preview.tombstones > 0 && (
              <div className="nx-alert nx-alert-info flex items-start gap-2">
                <IconInfo size={14} className="mt-0.5 shrink-0" />
                <div>
                  {t("包含 {tombstones} 条已删除记录，导入后本机对应资产将一并标记为已删除。", { tombstones: preview.tombstones })}
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
                {t("强制覆盖较新的本机条目")}
                <span className="nx-hint block">
                  {t("未勾选时，本机较新的资产将被跳过，不会被包内的旧版本覆盖。")}
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
                {importing ? t("正在导入…") : t("确认导入 {length} 条资产", { length: preview.bundle.assets.length })}
              </button>
            </div>
          </div>
        )}

        {report && <ImportReportView title={t("导入结果")} data={report} />}
      </div>
      )}
    </section>
  );
}

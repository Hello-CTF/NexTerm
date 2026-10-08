import { useId, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { sshImportApi, type SshImportItemAction, type SshImportSource } from "../../ipc/commands";
import type {
  SshImportApplyDto,
  SshImportHostDto,
  SshImportKeyDto,
  SshImportPreviewDto,
} from "../../ipc/types";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert, IconKey, IconServer, IconXCircle } from "../../ui/icons";
import { useVaultInitGate } from "../credentials/useVaultInitGate";

type Phase = "form" | "loading" | "preview" | "applying" | "done";

const ACTION_LABELS: Record<string, string> = {
  add: "新增",
  "skip-duplicate": "重复 · 跳过",
  "conflict-alias": "别名冲突",
  "conflict-endpoint": "地址冲突",
  "blocked-jump": "跳板机不可导入",
};

const DIAGNOSTIC_LABELS: Record<string, string> = {
  "host-pattern-skipped": "通配 Host 已跳过",
  "match-skipped": "Match 块已跳过",
  "key-unreadable": "文件无法读取",
  "symlink-unresolved": "符号链接无法解析",
  "symlink-escape": "符号链接指向目录外",
  "limit-truncated": "超出数量上限",
  "home-missing": "SSH 目录不存在",
  "config-unparsed": "配置文件解析失败",
  "termius-key-unparsed": "Termius 密钥无法解析",
  "unsupported-file": "不支持的文件类型",
  "encrypted-key-unreadable": "加密私钥无法读取",
};

function defaultAction(action: string): SshImportItemAction {
  return action === "add" ? "import" : "skip";
}

export function SshImportDialog({ onClose }: { onClose: () => void }) {
  const { pushToast } = useUi();
  const qc = useQueryClient();
  const [source, setSource] = useState<SshImportSource>("ssh-config");
  const [path, setPath] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [phase, setPhase] = useState<Phase>("form");
  const [preview, setPreview] = useState<SshImportPreviewDto | null>(null);
  const [hostActions, setHostActions] = useState<Record<string, SshImportItemAction>>({});
  const [keyActions, setKeyActions] = useState<Record<string, SshImportItemAction>>({});
  const [applyResult, setApplyResult] = useState<SshImportApplyDto | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { ensureVaultInit, vaultInitGate } = useVaultInitGate();

  const modalRef = useRef<HTMLDivElement>(null);
  const pathInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const pathId = useId();
  const confirmId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => pathInputRef.current,
  });

  const termius = source === "termius";
  // ssh-home 始终扫描默认 ~/.ssh，不携带其他来源页填写的隐藏路径
  const effectivePath = source === "ssh-home" ? undefined : path.trim() || undefined;

  const runPreview = async () => {
    setPhase("loading");
    setError(null);
    try {
      const result = await sshImportApi.preview({
        source,
        path: effectivePath,
        ...(termius ? { confirmed } : {}),
      });
      const hosts: Record<string, SshImportItemAction> = {};
      for (const host of result.hosts) hosts[host.id] = defaultAction(host.action);
      const keys: Record<string, SshImportItemAction> = {};
      for (const key of result.keys) keys[key.id] = defaultAction(key.action);
      setHostActions(hosts);
      setKeyActions(keys);
      setPreview(result);
      setPhase("preview");
    } catch (e) {
      setError(describeError(e));
      setPhase("form");
    }
  };

  const canApply =
    preview !== null &&
    [...Object.values(hostActions), ...Object.values(keyActions)].some(
      (action) => action !== "skip",
    );

  // 一键导入只提交当前仍为选中状态的可新增项；手动 skip 与冲突行的覆盖选择都不包含，覆盖需走「导入选中项」
  const quickImportHosts =
    preview === null
      ? []
      : preview.hosts.filter(
          (host) => host.action === "add" && (hostActions[host.id] ?? "skip") === "import",
        );
  const quickImportKeys =
    preview === null
      ? []
      : preview.keys.filter(
          (key) => key.action === "add" && (keyActions[key.id] ?? "skip") === "import",
        );
  const importableCount = quickImportHosts.length + quickImportKeys.length;

  const applySelection = async (
    hosts: { id: string; action: SshImportItemAction }[],
    keys: { id: string; action: SshImportItemAction }[],
  ) => {
    if (!preview) return;
    if (!(await ensureVaultInit())) return;
    setPhase("applying");
    setError(null);
    try {
      const result = await sshImportApi.apply({
        source,
        path: effectivePath,
        ...(termius ? { confirmed } : {}),
        hosts,
        keys,
      });
      setApplyResult(result);
      setPhase("done");
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
      pushToast(
        "success",
        `导入完成 · 新增 ${result.assetsCreated} 台主机 / ${result.credentialsCreated} 条密钥`,
      );
    } catch (e) {
      setError(describeError(e));
      setPhase("preview");
    }
  };

  const runApplySelection = () => {
    if (!preview) return;
    void applySelection(
      preview.hosts.map((host) => ({
        id: host.id,
        action: hostActions[host.id] ?? "skip",
      })),
      preview.keys.map((key) => ({
        id: key.id,
        action: keyActions[key.id] ?? "skip",
      })),
    );
  };

  const runQuickImport = () => {
    void applySelection(
      quickImportHosts.map((host) => ({ id: host.id, action: "import" as const })),
      quickImportKeys.map((key) => ({ id: key.id, action: "import" as const })),
    );
  };

  const backToForm = () => {
    setError(null);
    setPhase("form");
  };

  return (
    <>
      <div className="nx-overlay" onClick={phase === "applying" ? undefined : onClose}>
        <div
          ref={modalRef}
          className="nx-modal flex max-w-[560px] flex-col"
          role="dialog"
          aria-modal="true"
          aria-labelledby={titleId}
          tabIndex={-1}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => {
            e.stopPropagation();
            if (!layer.isTopmost()) return;
            if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e) && phase !== "applying") {
              e.preventDefault();
              onClose();
              return;
            }
            trapOverlayTab(e, modalRef.current);
          }}
        >
          <div className="nx-modal-header shrink-0">
            <span id={titleId} className="text-[13px] font-semibold text-neutral-100">
              导入 SSH 主机与密钥
            </span>
          </div>
          <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
            {error !== null && (
              <div className="mb-3 flex items-start gap-2 rounded-md border border-red-500/25 bg-red-500/10 px-3 py-2 text-[12px] text-red-300" role="alert">
                <IconXCircle size={14} className="mt-0.5 shrink-0" />
                <span className="min-w-0 flex-1">{error}</span>
                <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={backToForm}>
                  返回
                </button>
              </div>
            )}

            {phase === "form" && (
              <>
                <div className="nx-form-row">
                  <span className="nx-label">来源</span>
                  <div className="nx-segment mb-2">
                    <button
                      type="button"
                      className={`nx-segment-item ${source === "ssh-config" ? "is-active" : ""}`}
                      onClick={() => setSource("ssh-config")}
                    >
                      OpenSSH 配置
                    </button>
                    <button
                      type="button"
                      className={`nx-segment-item ${source === "ssh-home" ? "is-active" : ""}`}
                      onClick={() => setSource("ssh-home")}
                    >
                      ~/.ssh 目录
                    </button>
                    <button
                      type="button"
                      className={`nx-segment-item ${source === "termius" ? "is-active" : ""}`}
                      onClick={() => setSource("termius")}
                    >
                      Termius
                    </button>
                  </div>
                </div>

                {source === "ssh-config" && (
                  <div className="nx-form-row">
                    <label className="nx-label" htmlFor={pathId}>配置文件路径</label>
                    <input
                      id={pathId}
                      ref={pathInputRef}
                      className="nx-input font-mono text-[12px]"
                      value={path}
                      onChange={(e) => setPath(e.target.value)}
                      placeholder="~/.ssh/config"
                      autoComplete="off"
                    />
                    <span className="nx-hint mt-1.5 block">
                      留空使用默认路径。只在点「预览」时读取一次，不做后台扫描。
                    </span>
                  </div>
                )}

                {source === "ssh-home" && (
                  <div className="nx-form-row">
                    <span className="nx-hint block">
                      将扫描 ~/.ssh 目录：自动发现受支持的私钥，并解析 config 把 Host 与
                      IdentityFile 关联到对应密钥。只读取、不修改任何原始文件；跳过 .pub、
                      known_hosts、authorized_keys 等无关文件；私钥内容不会显示或写入日志。
                    </span>
                  </div>
                )}

                {termius && (
                  <div className="nx-form-row">
                    <label className="flex items-start gap-2 text-[12px] text-neutral-300" htmlFor={confirmId}>
                      <input
                        id={confirmId}
                        type="checkbox"
                        className="mt-0.5"
                        checked={confirmed}
                        onChange={(e) => setConfirmed(e.target.checked)}
                      />
                      <span>
                        我确认允许 NexTerm 读取本机 Termius 数据（仅本次，读取后展示预览，不会自动导入）
                      </span>
                    </label>
                    <span className="nx-hint mt-1.5 block">
                      需要从系统钥匙串取解密密钥，目前仅 macOS 可用；私钥与密码正文不会出现在预览里。
                    </span>
                  </div>
                )}
              </>
            )}

            {(phase === "loading" || phase === "applying") && (
              <div className="nx-empty" role="status">
                <div className="text-[12.5px] text-neutral-400">
                  {phase === "loading" ? "正在读取并生成预览…" : "正在导入…"}
                </div>
                <div className="nx-hint mt-1">
                  {phase === "loading" ? "仅本次读取，完成后立即展示结果" : "写入资产库与凭据库"}
                </div>
              </div>
            )}

            {phase === "preview" && preview !== null && (
              <PreviewBody
                preview={preview}
                hostActions={hostActions}
                keyActions={keyActions}
                onHostAction={(alias, action) =>
                  setHostActions((cur) => ({ ...cur, [alias]: action }))
                }
                onKeyAction={(name, action) =>
                  setKeyActions((cur) => ({ ...cur, [name]: action }))
                }
              />
            )}

            {phase === "done" && applyResult !== null && (
              <div role="status">
                <div className="mb-3 flex flex-wrap gap-1.5">
                  <span className="nx-badge nx-badge-green">新增主机 {applyResult.assetsCreated}</span>
                  <span className="nx-badge">覆盖主机 {applyResult.assetsUpdated}</span>
                  <span className="nx-badge nx-badge-green">新增密钥 {applyResult.credentialsCreated}</span>
                  <span className="nx-badge">覆盖密钥 {applyResult.credentialsUpdated}</span>
                  <span className="nx-badge">跳过 {applyResult.skipped}</span>
                </div>
                {applyResult.warnings.length > 0 && (
                  <div className="rounded-md border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-[12px] text-amber-300/90">
                    {applyResult.warnings.map((warning, index) => (
                      <div key={index}>{warning}</div>
                    ))}
                  </div>
                )}
                <div className="nx-hint mt-3">
                  已写入审计日志；可在资产树与凭据列表里查看新条目。
                </div>
              </div>
            )}
          </div>
          <div className="nx-modal-footer shrink-0">
            {phase === "preview" && (
              <button className="nx-btn nx-btn-ghost" onClick={backToForm}>
                返回
              </button>
            )}
            <div className="nx-spacer" />
            {phase !== "done" && phase !== "applying" && (
              <button className="nx-btn nx-btn-ghost" onClick={onClose}>
                取消
              </button>
            )}
            {phase === "form" && (
              <button
                className="nx-btn nx-btn-primary"
                disabled={termius && !confirmed}
                onClick={() => void runPreview()}
              >
                预览
              </button>
            )}
            {phase === "preview" && importableCount > 0 && (
              <button className="nx-btn nx-btn-ghost" onClick={runQuickImport}>
                一键导入全部可新增项 ({importableCount})
              </button>
            )}
            {phase === "preview" && (
              <button
                className="nx-btn nx-btn-primary"
                disabled={!canApply}
                onClick={runApplySelection}
              >
                导入选中项
              </button>
            )}
            {phase === "done" && (
              <button className="nx-btn nx-btn-primary" onClick={onClose}>
                完成
              </button>
            )}
          </div>
        </div>
      </div>
      {vaultInitGate}
    </>
  );
}

function PreviewBody({
  preview,
  hostActions,
  keyActions,
  onHostAction,
  onKeyAction,
}: {
  preview: SshImportPreviewDto;
  hostActions: Record<string, SshImportItemAction>;
  keyActions: Record<string, SshImportItemAction>;
  onHostAction: (alias: string, action: SshImportItemAction) => void;
  onKeyAction: (name: string, action: SshImportItemAction) => void;
}) {
  const importableHosts = preview.hosts.filter((host) => host.action === "add").length;
  const importableKeys = preview.keys.filter((key) => key.action === "add").length;
  if (preview.hosts.length === 0 && preview.keys.length === 0) {
    return (
      <div className="nx-empty">
        <div className="nx-empty-icon">
          <IconServer size={18} />
        </div>
        <div className="text-[12.5px] text-neutral-400">没有发现可导入的主机或密钥</div>
      </div>
    );
  }
  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center gap-1.5 text-[11.5px] text-neutral-500">
        <span>
          {preview.hosts.length} 台主机（{importableHosts} 台可新增）· {preview.keys.length} 个密钥（
          {importableKeys} 个可新增）
        </span>
        {preview.truncated && (
          <span className="nx-badge nx-badge-amber">数量超限已截断</span>
        )}
      </div>

      {preview.hosts.length > 0 && (
        <div className="mb-1 text-[11.5px] font-medium tracking-wide text-neutral-400">主机</div>
      )}
      <div className="mb-3 flex flex-col gap-1.5">
        {preview.hosts.map((host) => (
          <HostRow
            key={host.id}
            host={host}
            action={hostActions[host.id] ?? "skip"}
            onAction={(action) => onHostAction(host.id, action)}
          />
        ))}
      </div>

      {preview.keys.length > 0 && (
        <div className="mb-1 text-[11.5px] font-medium tracking-wide text-neutral-400">密钥</div>
      )}
      <div className="mb-3 flex flex-col gap-1.5">
        {preview.keys.map((key) => (
          <KeyRow
            key={key.id}
            item={key}
            action={keyActions[key.id] ?? "skip"}
            onAction={(action) => onKeyAction(key.id, action)}
          />
        ))}
      </div>

      {preview.diagnostics.length > 0 && (
        <>
          <div className="mb-1 text-[11.5px] font-medium tracking-wide text-neutral-400">诊断</div>
          <div className="flex flex-col gap-1 rounded-md border border-neutral-800/70 bg-neutral-950/40 px-3 py-2">
            {preview.diagnostics.map((diagnostic, index) => (
              <div key={index} className="flex items-start gap-2 text-[11.5px] text-neutral-500">
                <IconAlert size={12} className="mt-0.5 shrink-0" />
                <span>
                  <span className="text-neutral-400">{DIAGNOSTIC_LABELS[diagnostic.code] ?? diagnostic.code}</span> · {diagnostic.message}
                </span>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function StrategySelect({
  action,
  onAction,
  label,
}: {
  action: SshImportItemAction;
  onAction: (action: SshImportItemAction) => void;
  label: string;
}) {
  return (
    <select
      className="nx-input nx-input-sm shrink-0 font-mono text-[11.5px]"
      aria-label={label}
      value={action}
      onChange={(e) => onAction(e.target.value as SshImportItemAction)}
    >
      <option value="skip">跳过</option>
      <option value="overwrite">覆盖同名资产</option>
    </select>
  );
}

function ActionBadge({ action }: { action: string }) {
  return <span className="nx-badge shrink-0">{ACTION_LABELS[action] ?? action}</span>;
}

function Warnings({ warnings }: { warnings: string[] }) {
  if (warnings.length === 0) return null;
  return (
    <div className="mt-1 text-[11px] text-amber-300/80">
      {warnings.map((warning, index) => (
        <div key={index}>{warning}</div>
      ))}
    </div>
  );
}

function HostRow({
  host,
  action,
  onAction,
}: {
  host: SshImportHostDto;
  action: SshImportItemAction;
  onAction: (action: SshImportItemAction) => void;
}) {
  const selectable = host.action === "add";
  const conflict = host.action === "conflict-alias";
  return (
    <div className="rounded-md border border-neutral-800/70 bg-neutral-950/40 px-3 py-2">
      <div className="flex items-center gap-2">
        {selectable ? (
          <input
            type="checkbox"
            className="shrink-0"
            aria-label={`导入主机 ${host.alias}`}
            checked={action === "import"}
            onChange={(e) => onAction(e.target.checked ? "import" : "skip")}
          />
        ) : (
          <span className="w-4 shrink-0" />
        )}
        <span className="min-w-0 flex-1">
          <span className="mr-2 font-mono text-[12.5px] text-neutral-100">{host.alias}</span>
          <span className="text-[11.5px] text-neutral-500">
            {host.username ? `${host.username}@` : ""}
            {host.hostname}:{host.port}
          </span>
        </span>
        {host.proxyJump && (
          <span className="nx-badge shrink-0" title={`ProxyJump ${host.proxyJump}`}>
            跳板 {host.proxyJump}
          </span>
        )}
        {host.authMethod && (
          <span className="nx-badge shrink-0">
            {host.authMethod === "key" ? "密钥" : host.authMethod === "password" ? "密码" : "Agent"}
          </span>
        )}
        <ActionBadge action={host.action} />
        {conflict && (
          <StrategySelect
            action={action}
            onAction={onAction}
            label={`主机 ${host.alias} 的冲突处理`}
          />
        )}
      </div>
      <Warnings warnings={host.warnings} />
    </div>
  );
}

function KeyRow({
  item,
  action,
  onAction,
}: {
  item: SshImportKeyDto;
  action: SshImportItemAction;
  onAction: (action: SshImportItemAction) => void;
}) {
  const selectable = item.action === "add";
  const conflict = item.action === "conflict-alias";
  const name = item.aliases[0] ?? item.fingerprint;
  return (
    <div className="rounded-md border border-neutral-800/70 bg-neutral-950/40 px-3 py-2">
      <div className="flex items-center gap-2">
        {selectable ? (
          <input
            type="checkbox"
            className="shrink-0"
            aria-label={`导入密钥 ${name}`}
            checked={action === "import"}
            onChange={(e) => onAction(e.target.checked ? "import" : "skip")}
          />
        ) : (
          <span className="w-4 shrink-0" />
        )}
        <IconKey size={13} className="shrink-0 text-purple-400" />
        <span className="min-w-0 flex-1">
          <span className="mr-2 font-mono text-[12.5px] text-neutral-100">{name}</span>
          <span className="text-[11.5px] text-neutral-500">
            {item.keyType} · {item.fingerprint}
          </span>
        </span>
        {item.path && (
          <span className="hidden min-[460px]:inline truncate text-[11px] text-neutral-600" title={item.path}>
            {item.path}
          </span>
        )}
        <ActionBadge action={item.action} />
        {conflict && (
          <StrategySelect
            action={action}
            onAction={onAction}
            label={`密钥 ${name} 的冲突处理`}
          />
        )}
      </div>
      <Warnings warnings={item.warnings} />
    </div>
  );
}

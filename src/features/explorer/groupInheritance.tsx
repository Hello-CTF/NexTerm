import { useEffect, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { assetApi, vaultApi, type Asset, type AssetGroup, type AssetInheritance, type AssetInheritanceRequest, type GroupDefaults } from "../../ipc/commands";
import { Select } from "../../ui/Select";
import { useServerAccountRestrictions } from "../auth/useServerAccountRestrictions";
import { ServerRestrictionNote } from "../auth/ServerRestrictionNote";

// Kinds that read group connection defaults. The backend decides what each kind really inherits; this only
// avoids asking for a preview that can never show anything.
const INHERITING_KINDS = new Set(["ssh", "docker", "winrm", "rdp", "vnc"]);

export interface InheritanceInput {
  assetId?: string;
  kind: string;
  groupId: string | null;
  username: string;
  authKind: string;
  keyPath: string;
  credId: string;
  jumpAssetId: string;
  hasSecret: boolean;
  promptPassword?: boolean;
  enabled?: boolean;
}

export interface InheritanceView {
  data: AssetInheritance | undefined;
  // The default that would apply to an empty field, with its source group.
  applied: (field: "username" | "auth" | "jump") => { value: string; groupName: string } | null;
  // A default the group has but this asset cannot use, with the reason.
  skipped: (field: "username" | "auth" | "jump") => { reason: string; groupName: string } | null;
  error: string;
}

// Inheritance is resolved by the backend from the stored groups; the editor only displays the answer.
export function useAssetInheritance(input: InheritanceInput): InheritanceView {
  const active = input.enabled !== false && !!input.groupId && INHERITING_KINDS.has(input.kind);
  const query = useQuery({
    queryKey: [
      "asset-inheritance", input.assetId ?? "", input.kind, input.groupId, input.username.trim(), input.authKind,
      input.keyPath.trim(), input.credId, input.jumpAssetId, input.hasSecret, input.promptPassword ?? false,
    ],
    queryFn: () =>
      assetApi.inheritancePreview({
        assetId: input.assetId,
        kind: input.kind,
        groupId: input.groupId,
        username: input.username,
        authKind: input.authKind,
        keyPath: input.keyPath,
        credId: input.credId,
        jumpAssetId: input.jumpAssetId,
        hasSecret: input.hasSecret,
        promptPassword: input.promptPassword,
      }),
    enabled: active,
    refetchOnWindowFocus: false,
    staleTime: 0,
  });
  const data = active ? query.data : undefined;
  return {
    data,
    applied: (field) => {
      const item = data?.fields.find((entry) => entry.field === field && entry.applied);
      return item ? { value: item.value, groupName: item.groupName } : null;
    },
    skipped: (field) => {
      const item = data?.fields.find((entry) => (entry.field === field || entry.field === "all") && !entry.applied && entry.reason);
      return item ? { reason: item.reason, groupName: item.groupName } : null;
    },
    error: data?.error ?? "",
  };
}

// The line shown under an empty field: where the value would come from, or why the group's default is skipped.
export function InheritanceHint({ view, field, empty }: { view: InheritanceView; field: "username" | "auth" | "jump"; empty: boolean }) {
  if (!empty) return null;
  const applied = view.applied(field);
  const skipped = view.skipped(field);
  if (!applied && !skipped && !view.error) return null;
  return (
    <>
      {applied && (
        <div className="nx-hint mt-1.5" data-testid={`inherit-${field}`}>
          ↳ 留空则继承自 {applied.groupName}{applied.value ? `：${applied.value}` : ""}
        </div>
      )}
      {!applied && skipped && (
        <div className="nx-hint mt-1.5 text-amber-300" data-testid={`inherit-skipped-${field}`}>
          ↳ 已跳过 {skipped.groupName} 的默认值：{skipped.reason}
        </div>
      )}
      {!applied && !skipped && view.error && (
        <div className="mt-1.5 text-[11px] text-red-400" role="alert">↳ {view.error}</div>
      )}
    </>
  );
}

export function inheritedPlaceholder(view: InheritanceView, field: "username" | "auth" | "jump", fallback?: string): string | undefined {
  const applied = view.applied(field);
  if (applied) return `${applied.value}（继承自 ${applied.groupName}）`;
  return fallback;
}

export interface GroupDefaultsState {
  username: string;
  authKind: string;
  keyPath: string;
  credId: string;
  jumpAssetId: string;
}

export function groupDefaultsFrom(group?: AssetGroup | null): GroupDefaultsState {
  return {
    username: group?.defaultUsername ?? "",
    authKind: group?.defaultAuthKind ?? "",
    keyPath: group?.defaultKeyPath ?? "",
    credId: group?.defaultCredId ?? "",
    jumpAssetId: group?.defaultJumpAssetId ?? "",
  };
}

export function groupDefaultsRequest(state: GroupDefaultsState): GroupDefaults {
  return {
    username: state.username.trim(),
    authKind: state.authKind,
    keyPath: state.authKind === "key" ? state.keyPath.trim() : "",
    credId: state.authKind === "agent" ? "" : state.credId,
    jumpAssetId: state.jumpAssetId,
  };
}

// The 连接默认值 section of the group editor. Assets in the group (and its subgroups) that leave a field empty use it.
export function GroupDefaultsFields({
  value,
  onChange,
  groupId,
}: {
  value: GroupDefaultsState;
  onChange: (next: GroupDefaultsState) => void;
  groupId: string | null;
}) {
  const ids = useId();
  const { restricted: serverLimited, text: serverText } = useServerAccountRestrictions();
  const credentials = useQuery({ queryKey: ["credentials"], queryFn: () => vaultApi.listCredentials(), refetchOnWindowFocus: false });
  const assets = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), refetchOnWindowFocus: false });
  const patch = (partial: Partial<GroupDefaultsState>) => onChange({ ...value, ...partial });
  const credList = credentials.data ?? [];
  const wantsKey = value.authKind === "key";
  const credOptions = credList.filter((c) => (wantsKey ? c.kind === "private_key" || c.kind === "passphrase" : c.kind !== "private_key"));
  const jumpCandidates = (assets.data ?? []).filter((a: Asset) => a.kind === "ssh" || a.kind === "docker");
  const [open, setOpen] = useState(
    !!(value.username || value.authKind || value.credId || value.jumpAssetId),
  );
  useEffect(() => {
    if (value.username || value.authKind || value.credId || value.jumpAssetId) setOpen(true);
  }, [value.username, value.authKind, value.credId, value.jumpAssetId]);
  const missingCred = !!value.credId && credentials.isSuccess && !credList.some((c) => c.id === value.credId);
  const missingJump = !!value.jumpAssetId && assets.isSuccess && !jumpCandidates.some((a) => a.id === value.jumpAssetId);
  void groupId;
  return (
    <fieldset className="mt-3 border-t border-neutral-800/60 pt-2.5">
      <legend className="sr-only">连接默认值</legend>
      <button
        type="button"
        className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
        aria-expanded={open}
        aria-controls={`${ids}-panel`}
        onClick={() => setOpen((v) => !v)}
      >
        连接默认值
      </button>
      <div className="nx-hint mt-1">
        ↳ 本分组及子分组内的资产在对应字段留空时继承这里的值；资产自己填写的值优先，离资产最近的分组优先。端口、主机和协议选项不继承。
      </div>
      {open && (
        <div id={`${ids}-panel`} className="mt-2.5">
          <div className="nx-form-row">
            <label className="nx-label" htmlFor={`${ids}-user`}>默认用户名</label>
            <input
              id={`${ids}-user`}
              className="nx-input"
              value={value.username}
              onChange={(e) => patch({ username: e.target.value })}
              placeholder="留空则不设置"
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label">默认认证</label>
            <Select
              ariaLabel="默认认证"
              value={value.authKind}
              onChange={(next) => patch({ authKind: next, credId: next === "agent" ? "" : value.credId, keyPath: next === "key" ? value.keyPath : "" })}
              options={[
                { value: "", label: "（不设置）" },
                { value: "password", label: "密码" },
                { value: "keyboard-interactive", label: "键盘交互（密码）" },
                { value: "key", label: "私钥" },
                { value: "agent", label: serverLimited ? "SSH Agent（多用户服务端不可用）" : "SSH Agent", disabled: serverLimited },
              ]}
            />
            {value.authKind === "key" && (
              <input
                className="nx-input mt-1.5 font-mono text-[12px]"
                aria-label="默认私钥路径"
                disabled={serverLimited}
                value={value.keyPath}
                onChange={(e) => patch({ keyPath: e.target.value })}
                placeholder="私钥文件路径（或在下方选择私钥凭据）"
              />
            )}
            {serverLimited && value.authKind === "agent" && <ServerRestrictionNote text={serverText.agentAuth} />}
            {serverLimited && value.authKind === "key" && (
              <ServerRestrictionNote
                text={serverText.keyPath}
                onClear={value.keyPath ? () => patch({ keyPath: "" }) : undefined}
              />
            )}
            {value.authKind !== "" && value.authKind !== "agent" && (
              <Select
                className="mt-1.5"
                ariaLabel="默认凭据"
                value={value.credId}
                onChange={(next) => patch({ credId: next })}
                options={[
                  { value: "", label: wantsKey ? "（不使用凭据）" : "选择密码凭据…" },
                  ...(missingCred ? [{ value: value.credId, label: "当前凭据（不在可选列表）" }] : []),
                  ...credOptions.map((c) => ({ value: c.id, label: c.name })),
                ]}
              />
            )}
            {value.authKind !== "" && (
              <div className="nx-hint mt-1.5">
                ↳ 认证是一个整体：资产自己设置了任何认证项，就不会继承这里的认证。RDP、VNC 和 WinRM 只使用密码凭据，私钥与 Agent 默认会被它们跳过。
              </div>
            )}
          </div>
          <div className="nx-form-row">
            <label className="nx-label">默认跳板机</label>
            <Select
              ariaLabel="默认跳板机"
              value={value.jumpAssetId}
              onChange={(next) => patch({ jumpAssetId: next })}
              options={[
                { value: "", label: "（不设置）" },
                ...(missingJump ? [{ value: value.jumpAssetId, label: "当前跳板机（已删除或不可用）" }] : []),
                ...jumpCandidates.map((a) => ({ value: a.id, label: `${a.name}（${a.host}:${a.port}）` })),
              ]}
            />
            <div className="nx-hint mt-1.5">
              ↳ SSH 与 Docker 资产用作跳板机，RDP 与 VNC 资产经它转发；跳板机自己所在的资产不会再经由自己跳转。
            </div>
            {missingJump && <div className="mt-1.5 text-[11px] text-red-400" role="alert">↳ 该跳板机已删除，连接会报错，请重新选择。</div>}
            {missingCred && <div className="mt-1.5 text-[11px] text-red-400" role="alert">↳ 默认凭据不存在，连接会报错，请重新选择。</div>}
          </div>
        </div>
      )}
    </fieldset>
  );
}

const FIELD_LABEL: Record<string, string> = { auth: "认证方式", jump: "跳板机" };

function previewRequest(asset: Asset, groupId: string | null): AssetInheritanceRequest {
  const options = asset.options ?? {};
  const text = (key: string) => (typeof options[key] === "string" ? (options[key] as string) : "");
  const remote = asset.kind === "rdp" || asset.kind === "vnc";
  return {
    assetId: asset.id,
    kind: asset.kind,
    groupId,
    username: asset.username ?? "",
    authKind: asset.authKind ?? "",
    keyPath: asset.keyPath ?? "",
    credId: asset.credId ?? "",
    jumpAssetId: remote ? text("viaAssetId") : text("jumpAssetId"),
    hasSecret: text("password") !== "",
    promptPassword: remote && options.promptPassword === true,
  };
}

// The credential, authentication or jump route an asset would newly inherit, or inherit differently, when it moves
// to another group, as the backend resolves them. Empty when the move changes nothing about its identity.
export async function inheritanceChangesOnMove(asset: Asset, targetGroupId: string | null): Promise<string[]> {
  if (asset.groupId === targetGroupId) return [];
  try {
    const [before, after] = await Promise.all([
      asset.groupId ? assetApi.inheritancePreview(previewRequest(asset, asset.groupId)) : Promise.resolve(null),
      targetGroupId ? assetApi.inheritancePreview(previewRequest(asset, targetGroupId)) : Promise.resolve(null),
    ]);
    const changes: string[] = [];
    for (const field of ["auth", "jump"] as const) {
      const next = after?.fields.find((item) => item.field === field && item.applied);
      const previous = before?.fields.find((item) => item.field === field && item.applied);
      if (next && (!previous || previous.groupId !== next.groupId || previous.value !== next.value)) {
        changes.push(`${FIELD_LABEL[field]}改为「${next.value || "继承值"}」（来自 ${next.groupName}）`);
      } else if (!next && previous) {
        changes.push(`${FIELD_LABEL[field]}不再继承自 ${previous.groupName}`);
      }
    }
    return changes;
  } catch {
    // A preview that cannot be had must not block the move; the connect reports any real problem.
    return [];
  }
}

export function moveConfirmationText(lines: { name: string; targetName: string; changes: string[] }[]): string {
  const body = lines.map((line) => `「${line.name}」移入 ${line.targetName} 后：${line.changes.join("；")}`).join("\n");
  return `移动会改变这些资产实际使用的连接方式：\n\n${body}\n\n仍要移动吗？`;
}

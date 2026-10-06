// 资产批量导出到剪贴板。
//
// 刻意与 CredentialsView（只读、不含明文、纯前端拼装）分开成另一个面：
// 用户明确要的是**带明文密码**的行格式，放进只读文本视图会把那条边界踩坏。
//
// 三条安全约定（跟 CredentialsPanel 的「显示明文」同源）：
// · 明文只在**点导出的那一刻**用 `vaultApi.revealCredential` 现取，不进 state、不留缓存；
// · 锁定态先走既有的 useVaultUnlock 解锁流程，不绕过密码保护；
// · 导出前明确提示"内容含明文、会留在系统剪贴板里"。
//
// 行格式（每行一条，字段顺序固定）：
//   host=<h> port=<p> user=<u> authType=<authKind> password=<明文> title=<name> note=<note>
import { useMemo, useState } from "react";
import { vaultApi, type Asset, type Credential } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { useVaultUnlock } from "./useVaultUnlock";
import { assetIcon, IconClose, IconCopy, IconLock } from "../../ui/icons";

/**
 * 可导出的资产类型。
 *
 * 这个行格式是"登录 + 密码"的 SSH 语义：`ssh` 自不必说，`docker` 主机也是走 SSH 连的，
 * `winrm` 同样是"主机 + 账号 + 密码"（authType=ntlm）。其余：
 * · `local`（内置"当前设备"）没有 host，也没有登录凭据，无内容可导；
 * · `mysql` / `redis` 是数据库连接串语义（库名、字符集等），硬塞进这一行只会误导。
 * 所以只收上面三类，且必须已填 host。
 */
const EXPORT_KINDS = new Set(["ssh", "docker", "winrm"]);

/** 值里含空白才用双引号包起来（对齐用户示例）；引号本身转义。 */
function quoteValue(raw: string): string {
  const v = raw.replace(/[\r\n\t]+/g, " ");
  return /\s/.test(v) ? `"${v.replace(/"/g, '\\"')}"` : v;
}

/**
 * 拼一行导出文本。导出成纯函数，格式规则一眼可查、也便于日后单测。
 *
 * `revealedPassword` 只在"绑定的凭据本身是密码类"时才有意义 ——
 * 私钥类凭据的正文不是登录密码，不能填进 password 字段。
 */
export function buildExportLine(
  asset: Asset,
  cred: Credential | undefined,
  revealedPassword: string | undefined,
): string {
  const password = cred && cred.kind === "password" ? (revealedPassword ?? "") : "";
  return [
    `host=${quoteValue(asset.host ?? "")}`,
    `port=${asset.port ?? ""}`,
    `user=${quoteValue(asset.username ?? "")}`,
    `authType=${quoteValue(asset.authKind ?? "")}`,
    // password 一律不加引号（对齐用户示例）
    `password=${password}`,
    `title=${quoteValue(asset.name)}`,
    `note=${quoteValue(asset.note ?? "")}`,
  ].join(" ");
}

export function ExportAssetsModal({
  assets,
  credentials,
  onClose,
}: {
  assets: Asset[];
  credentials: Credential[];
  onClose: () => void;
}) {
  const { pushToast } = useUi();
  const unlock = useVaultUnlock();
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);

  const credsById = useMemo(() => new Map(credentials.map((c) => [c.id, c])), [credentials]);
  const list = useMemo(
    () => assets.filter((a) => EXPORT_KINDS.has(a.kind) && !!a.host),
    [assets],
  );
  const allPicked = list.length > 0 && list.every((a) => picked.has(a.id));
  /** 绑定的是密码类凭据 —— 只有这些资产导出时才会去取明文。 */
  const hasPassword = (a: Asset) => {
    const c = a.credId ? credsById.get(a.credId) : undefined;
    return !!c && c.kind === "password";
  };

  const toggle = (id: string, on: boolean) => {
    setPicked((cur) => {
      const next = new Set(cur);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const run = async () => {
    const pickedAssets = list.filter((a) => picked.has(a.id));
    if (pickedAssets.length === 0) return;
    setBusy(true);
    try {
      // 明文受凭据库保护：锁定态先走既有解锁流程，绝不绕过。
      const st = await vaultApi.status();
      if (st.initialized && !st.unlocked) {
        const ok = await unlock("导出包含明文密码，请先解锁凭据库：");
        if (!ok) return;
      }

      // 只对「绑定了密码类凭据」的资产现取明文；同一条凭据只 reveal 一次。
      const need = new Set(
        pickedAssets.filter(hasPassword).map((a) => a.credId as string),
      );
      const revealed = new Map<string, string>();
      await Promise.all(
        [...need].map(async (id) => {
          const r = await vaultApi.revealCredential(id);
          revealed.set(id, r.value);
        }),
      );

      const text = pickedAssets
        .map((a) =>
          buildExportLine(
            a,
            a.credId ? credsById.get(a.credId) : undefined,
            a.credId ? revealed.get(a.credId) : undefined,
          ),
        )
        .join("\n");
      await navigator.clipboard.writeText(text);
      pushToast("success", `已导出 ${pickedAssets.length} 个资产到剪贴板`);
      onClose();
    } catch (e) {
      pushToast("error", `导出失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[560px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">导出资产到剪贴板</span>
          <div className="nx-spacer" />
          <button className="nx-icon-btn nx-icon-btn-sm" title="关闭" onClick={onClose}>
            <IconClose size={14} />
          </button>
        </div>

        <div className="nx-modal-body">
          {/* 明文警告：导出的是"能直接登录"的东西，粘贴前必须知道它落在剪贴板里 */}
          <div className="mb-3 flex items-start gap-2 rounded-md border border-amber-500/25 bg-amber-500/10 px-2.5 py-2 text-[11.5px] text-amber-300/90">
            <IconLock size={13} className="mt-[1px] shrink-0" />
            <span>
              导出内容含<span className="font-semibold">明文密码</span>，会留在系统剪贴板中。
              请勿粘贴到聊天、工单等不可信的地方。
            </span>
          </div>

          {/* 工具条：全选 + 选中计数 */}
          <div className="mb-2 flex items-center gap-2">
            <label className="flex cursor-pointer items-center gap-2 text-[12px] text-neutral-400">
              <input
                className="nx-check"
                type="checkbox"
                aria-label="全选可导出资产"
                checked={allPicked}
                ref={(el) => {
                  if (el) el.indeterminate = picked.size > 0 && !allPicked;
                }}
                onChange={(e) =>
                  setPicked(e.target.checked ? new Set(list.map((a) => a.id)) : new Set())
                }
              />
              全选
            </label>
            <div className="nx-spacer" />
            <span className="text-[11.5px] text-neutral-500">
              已选 <span className="text-neutral-300">{picked.size}</span> / {list.length}
            </span>
          </div>

          {/* 列表 */}
          <div className="max-h-[46vh] overflow-y-auto rounded-lg border border-neutral-800/70 bg-neutral-950/40 p-1">
            {list.length === 0 ? (
              <div className="nx-hint px-3 py-8 text-center">
                没有可导出的资产
                <br />
                仅支持已填主机地址的 SSH / Docker / WinRM 资产
              </div>
            ) : (
              list.map((a) => {
                const Icon = assetIcon(a.kind);
                const on = picked.has(a.id);
                const withPwd = hasPassword(a);
                const endpoint = `${a.username ? `${a.username}@` : ""}${a.host}${
                  a.port ? `:${a.port}` : ""
                }`;
                return (
                  <label
                    key={a.id}
                    className={`flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 ${
                      on ? "bg-blue-500/[.08]" : "hover:bg-white/[.04]"
                    }`}
                  >
                    <input
                      className="nx-check"
                      type="checkbox"
                      aria-label={`选择 ${a.name}`}
                      checked={on}
                      onChange={(e) => toggle(a.id, e.target.checked)}
                    />
                    <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-white/[.06] text-neutral-400">
                      <Icon size={13} />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[12.5px] text-neutral-200">
                        {a.name}
                      </span>
                      <span className="nx-mono block truncate text-[11px] text-neutral-500">
                        {endpoint}
                      </span>
                    </span>
                    <span className="flex shrink-0 items-center gap-1.5">
                      <span className="nx-badge">{a.authKind ?? "未设置"}</span>
                      {withPwd ? (
                        <span className="nx-badge nx-badge-green">含密码</span>
                      ) : (
                        <span className="nx-badge">无密码</span>
                      )}
                    </span>
                  </label>
                );
              })
            )}
          </div>

          <div className="nx-hint mt-2">
            每行一条：<code>host= port= user= authType= password= title= note=</code>；
            「无密码」的资产其 password 字段留空。本机与数据库资产不在这个格式内。
          </div>
        </div>

        <div className="nx-modal-footer">
          <button className="nx-btn nx-btn-ghost" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={picked.size === 0 || busy}
            onClick={() => void run()}
          >
            <IconCopy size={12} />
            {busy ? "导出中…" : `导出到剪贴板${picked.size > 0 ? ` (${picked.size})` : ""}`}
          </button>
        </div>
      </div>
    </div>
  );
}

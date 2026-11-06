// SyncScopeBadge marks where a setting is kept: "sync" follows the signed-in account to its other devices, "local"
// stays on this device. It replaces per-setting sentences saying the same thing.
export function SyncScopeBadge({ scope }: { scope: "sync" | "local" }) {
  return scope === "sync" ? (
    <span className="nx-badge" title="登录账号后随账号同步到其他设备" data-sync-scope="sync">
      同步
    </span>
  ) : (
    <span className="nx-badge" title="仅保存在本机，不随账号同步" data-sync-scope="local">
      本机
    </span>
  );
}

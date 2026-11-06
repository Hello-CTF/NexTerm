// The scope filter of the listening-ports list. Free of imports so the website demo can mirror it.

export type PortScope = "all" | "exposed" | "loopback";

export const PORT_SCOPES: readonly PortScope[] = ["all", "exposed", "loopback"];

export const SCOPE_LABELS: Record<PortScope, string> = { all: "全部", exposed: "对外", loopback: "仅本机" };

export const SCOPE_TITLES: Record<PortScope, string> = {
  all: "所有监听端口",
  exposed: "绑定在通配地址或具体网卡地址上，其他机器可能访问到",
  loopback: "只绑定在 127.0.0.1 或 ::1 上，只有本机能访问",
};

// isLoopback reports a listener only this host can reach.
export function isLoopback(address: string): boolean {
  const bare = address.replace(/^\[|\]$/g, "").replace(/%.*$/, "").toLowerCase();
  return bare === "localhost" || bare === "::1" || bare.startsWith("127.") || bare === "::ffff:127.0.0.1";
}

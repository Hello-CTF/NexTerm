/** @vitest-environment jsdom */
// FLEET168 one-line install command for brand-new machines: construction of oneLineInstallCommand (curl fetches
// /install-device.sh + --server/--code/--version real version + --insecure follows
// the enrollment URL, dynamic params single-quote wrapped), Windows devices additionally get the PowerShell version
// oneLineInstallCommandWindows (iex fetches /install-device.ps1, same-named params),
// plus the one-line install command in the DevicesView enrollment panel - the version
// comes from the real /healthz, the whole block is hidden when the version is unavailable (no faking), existing
// enroll/install commands are kept, and install success is never claimed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { DevicesView } from "../../features/fleet/DevicesView";
import { oneLineInstallCommand, oneLineInstallCommandWindows, psQuote, shellQuote } from "../../features/fleet/installCommand";
import { useAuth } from "../../features/auth/store";

const NOW = Date.now();

const SUPERADMIN = {
  id: "u-1",
  username: "root",
  display_name: "Root",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const AGENT_DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: NOW - 86400_000,
  last_seen_at: NOW - 30_000,
  revoked_at: 0,
  owner: { id: "u-1", username: "alice" },
  agent: {
    platform: "linux",
    app_version: "0.2.2",
    desired_autostart: true,
    terminal_enabled: true,
    current_url: "https://nexterm.example.com",
    service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
    last_seen_at: NOW - 30_000,
  },
};

const BASE_URLS = [{ url: "https://nexterm.example.com" }, { url: "https://10.0.0.8:8443", insecure: true }];

interface RecordedCall {
  url: string;
  method: string;
}

// healthBehavior controls GET /healthz: ok returns a real version, fail simulates server unavailability,
// empty returns an empty version (in none of these may the one-line install command appear).
const healthBehavior: { mode: "ok" | "fail" | "empty" } = { mode: "ok" };

function route(): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    calls.push({ url: u, method });
    if (u === "/fleet/devices" && method === "GET") {
      return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [AGENT_DEVICE] }) };
    }
    if (u === "/fleet/base-urls" && method === "GET") {
      return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: BASE_URLS }) };
    }
    if (u === "/healthz" && method === "GET") {
      if (healthBehavior.mode === "fail") {
        return { ok: false, status: 500, text: async () => JSON.stringify({ error: { code: "internal", message: "boom" } }) };
      }
      const version = healthBehavior.mode === "empty" ? "" : "0.2.2";
      return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, service: "nexterm-server", version }) };
    }
    if (u === "/device/enroll-codes" && method === "POST") {
      return { ok: true, status: 200, text: async () => JSON.stringify({ code: "fleet-code-1", expires_at: NOW + 900_000 }) };
    }
    return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
  });
  vi.stubGlobal("fetch", mocks.fetch);
  return calls;
}

let mounted: MountedView | undefined;

function seedUser(): void {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: SUPERADMIN,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  healthBehavior.mode = "ok";
  document.body.replaceChildren();
  seedUser();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

async function openIssuedPanel(): Promise<void> {
  mounted = mountWithClient(createElement(DevicesView));
  await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
  click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
  click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "签发接入码") as HTMLButtonElement);
  await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);
}

function commandTexts(): string[] {
  return [...document.querySelectorAll("code")].map((c) => c.textContent ?? "");
}

describe("oneLineInstallCommand 构造", () => {
  it("curl 拉取接入地址的 install-device.sh 并带真实 --server/--code/--version", () => {
    expect(oneLineInstallCommand("https://nexterm.example.com", false, "c-1", "0.2.2")).toBe(
      "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
        "--server 'https://nexterm.example.com' --code 'c-1' --version '0.2.2'",
    );
  });

  it("非 insecure 的接入地址两侧都严格校验证书, 不出现任何 --insecure", () => {
    const command = oneLineInstallCommand("https://nexterm.example.com", false, "c-1", "0.2.2");
    expect(command).toBe(
      "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
        "--server 'https://nexterm.example.com' --code 'c-1' --version '0.2.2'",
    );
    expect(command).not.toContain("--insecure");
  });

  it("显式 insecure 的自签名 HTTPS 接入地址: curl 与脚本/enroll 两侧都放宽证书", () => {
    const command = oneLineInstallCommand("https://10.0.0.8:8443", true, "c-1", "0.2.2");
    expect(command).toBe(
      "curl -fsSL --insecure 'https://10.0.0.8:8443/install-device.sh' | sh -s -- " +
        "--server 'https://10.0.0.8:8443' --code 'c-1' --version '0.2.2' --insecure",
    );
  });

  it("动态参数中的 shell 元字符被单引号包裹, 单引号自身转义", () => {
    const command = oneLineInstallCommand("https://nexterm.example.com/mirror;v2/$(id)", false, "co'de", "0.2.2");
    expect(command).toContain("'https://nexterm.example.com/mirror;v2/$(id)/install-device.sh'");
    expect(command).toContain("--code 'co'\\''de'");
    // The constructed result contains no unwrapped metacharacters overall (each argument remains single-quote safe)
    expect(shellQuote("a'b")).toBe("'a'\\''b'");
  });
});

describe("oneLineInstallCommandWindows 构造", () => {
  it("iex 拉取接入地址的 install-device.ps1 并透传与 sh 版相同的参数", () => {
    expect(oneLineInstallCommandWindows("https://nexterm.example.com", false, "c-1", "0.2.2")).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://nexterm.example.com/install-device.ps1') } " +
        "--server 'https://nexterm.example.com' --code 'c-1' --version '0.2.2'\"",
    );
  });

  it("显式 insecure 的接入地址: --insecure 透传给脚本参数", () => {
    const command = oneLineInstallCommandWindows("https://10.0.0.8:8443", true, "c-1", "0.2.2");
    expect(command).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://10.0.0.8:8443/install-device.ps1') } " +
        "--server 'https://10.0.0.8:8443' --code 'c-1' --version '0.2.2' --insecure\"",
    );
  });

  it("动态参数按 PowerShell 字面字符串引用, 内嵌单引号双写转义", () => {
    const command = oneLineInstallCommandWindows("https://nexterm.example.com/mirror;v2/$(id)", false, "co'de", "0.2.2");
    expect(command).toContain("'https://nexterm.example.com/mirror;v2/$(id)/install-device.ps1'");
    expect(command).toContain("--code 'co''de'");
    expect(psQuote("a'b")).toBe("'a''b'");
  });
});

describe("设备管理 · 一键安装指令", () => {
  it("签发后按接入地址展示一行安装指令 (真实 /healthz 版本), 既有 enroll/install 命令保留", async () => {
    const calls = route();
    await openIssuedPanel();

    expect(calls.some((c) => c.url === "/healthz")).toBe(true);
    const commands = commandTexts();
    const oneLiners = commands.filter((c) => c.includes("install-device.sh"));
    expect(oneLiners).toHaveLength(2);
    expect(oneLiners[0]).toBe(
      "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
        "--server 'https://nexterm.example.com' --code 'fleet-code-1' --version '0.2.2'",
    );
    expect(oneLiners[1]).toBe(
      "curl -fsSL --insecure 'https://10.0.0.8:8443/install-device.sh' | sh -s -- " +
        "--server 'https://10.0.0.8:8443' --code 'fleet-code-1' --version '0.2.2' --insecure",
    );
    const details = document.querySelector<HTMLDetailsElement>("details");
    const oneLineElements = [...document.querySelectorAll("code")].filter((c) => c.textContent?.includes("install-device.sh"));
    expect(details).not.toBeNull();
    expect(details?.open).toBe(false);
    expect(oneLineElements[0].closest("details")).toBeNull();
    expect(oneLineElements[1].closest("details")).toBe(details);
    // Windows PowerShell version hint and command: the first address is in the main area, the fallback address goes into the advanced details
    expect(document.body.textContent).toContain("Windows 设备请用 PowerShell 版");
    const windowsOneLiners = commands.filter((c) => c.includes("install-device.ps1"));
    expect(windowsOneLiners).toHaveLength(2);
    expect(windowsOneLiners[0]).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://nexterm.example.com/install-device.ps1') } " +
        "--server 'https://nexterm.example.com' --code 'fleet-code-1' --version '0.2.2'\"",
    );
    expect(windowsOneLiners[1]).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://10.0.0.8:8443/install-device.ps1') } " +
        "--server 'https://10.0.0.8:8443' --code 'fleet-code-1' --version '0.2.2' --insecure\"",
    );
    const windowsElements = [...document.querySelectorAll("code")].filter((c) => c.textContent?.includes("install-device.ps1"));
    expect(windowsElements[0].closest("details")).toBeNull();
    expect(windowsElements[1].closest("details")).toBe(details);
    // Existing commands (binary already installed) kept
    expect(commands.some((c) => c.includes("nexterm-agent enroll"))).toBe(true);
    expect(commands.some((c) => c.includes("nexterm-agent install"))).toBe(true);
    // Install success is never claimed
    expect(document.body.textContent).toContain("不会执行安装");
    expect(document.body.textContent).toContain("无法确认安装结果");
  });

  it("接入地址含 shell 元字符时一行安装指令同样单引号安全", async () => {
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/fleet/devices" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [AGENT_DEVICE] }) };
      }
      if (u === "/fleet/base-urls" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: [{ url: "https://nexterm.example.com/mirror;v2/$(id)" }] }) };
      }
      if (u === "/healthz" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, version: "0.2.2" }) };
      }
      if (u === "/device/enroll-codes" && method === "POST") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ code: "fleet-code-1", expires_at: NOW + 900_000 }) };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    await openIssuedPanel();

    const oneLiner = commandTexts().find((c) => c.includes("install-device.sh"));
    expect(oneLiner).toContain("curl -fsSL 'https://nexterm.example.com/mirror;v2/$(id)/install-device.sh'");
    expect(oneLiner).toContain("--server 'https://nexterm.example.com/mirror;v2/$(id)'");
  });

  it("版本不可用 (/healthz 失败) 时一键安装指令整段隐藏, 不伪造版本", async () => {
    healthBehavior.mode = "fail";
    route();
    await openIssuedPanel();

    expect(commandTexts().some((c) => c.includes("install-device.sh"))).toBe(false);
    expect(commandTexts().some((c) => c.includes("install-device.ps1"))).toBe(false);
    expect(document.body.textContent).not.toContain("--version");
    expect(document.body.textContent).not.toContain("PowerShell");
    // Existing commands are unaffected
    expect(commandTexts().some((c) => c.includes("nexterm-agent enroll"))).toBe(true);
  });

  it("版本为空串时同样隐藏一键安装指令", async () => {
    healthBehavior.mode = "empty";
    route();
    await openIssuedPanel();

    expect(commandTexts().some((c) => c.includes("install-device.sh"))).toBe(false);
    expect(commandTexts().some((c) => c.includes("install-device.ps1"))).toBe(false);
  });
});

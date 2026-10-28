/** @vitest-environment jsdom */
// FLEET168 one-line install command for brand-new machines: construction of oneLineInstallCommand (curl fetches
// /install-device.sh + --server/--code/--version real version + --insecure follows
// the enrollment URL, dynamic params single-quote wrapped), Windows devices additionally get the PowerShell version
// oneLineInstallCommandWindows (iex fetches /install-device.ps1, same-named params),
// plus the one-line install command in the DevicesView enrollment panel - the version
// comes from the real /healthz, the whole block is hidden when the version is unavailable (no faking), existing
// enroll/install commands are kept, and install success is never claimed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
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
import { oneLineInstallCommand, oneLineInstallCommandWindows, psQuote, releaseOrNull, shellQuote } from "../../features/fleet/installCommand";
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

const NEW_DEVICE = {
  ...AGENT_DEVICE,
  id: "d-2",
  name: "new-host",
};

const BASE_URLS = [{ url: "https://nexterm.example.com" }, { url: "https://10.0.0.8:8443", insecure: true }];

interface RecordedCall {
  url: string;
  method: string;
}

// healthBehavior controls GET /healthz: ok returns a real version, fail simulates server unavailability,
// empty and dev return versions that cannot exist as releases (in none of these may the one-line install command appear).
const healthBehavior: { mode: "ok" | "fail" | "empty" | "dev" } = { mode: "ok" };

// scenario is mutated by tests to change what the mocked server returns on the next request.
const scenario: { devices: unknown[]; ttlMs: number; issued: number } = { devices: [AGENT_DEVICE], ttlMs: 900_000, issued: 0 };

function route(): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    calls.push({ url: u, method });
    if (u === "/fleet/devices" && method === "GET") {
      return { ok: true, status: 200, text: async () => JSON.stringify({ devices: scenario.devices }) };
    }
    if (u === "/fleet/base-urls" && method === "GET") {
      return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: BASE_URLS }) };
    }
    if (u === "/healthz" && method === "GET") {
      if (healthBehavior.mode === "fail") {
        return { ok: false, status: 500, text: async () => JSON.stringify({ error: { code: "internal", message: "boom" } }) };
      }
      const version = healthBehavior.mode === "empty" ? "" : healthBehavior.mode === "dev" ? "dev" : "0.2.2";
      return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, service: "nexterm-server", version }) };
    }
    if (u === "/device/enroll-codes" && method === "POST") {
      scenario.issued += 1;
      return {
        ok: true,
        status: 200,
        text: async () => JSON.stringify({ code: `fleet-code-${scenario.issued}`, expires_at: Date.now() + scenario.ttlMs }),
      };
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
  scenario.devices = [AGENT_DEVICE];
  scenario.ttlMs = 900_000;
  scenario.issued = 0;
  document.body.replaceChildren();
  seedUser();
});

afterEach(() => {
  mounted?.unmount();
  vi.useRealTimers();
  mounted = undefined;
  vi.unstubAllGlobals();
});

async function openIssuedPanel(): Promise<void> {
  mounted = mountWithClient(createElement(DevicesView));
  await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
  click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
  await flushUntil(() => document.body.textContent?.includes("等待设备接入") ?? false);
}

function commandTexts(): string[] {
  return [...document.querySelectorAll("code")].map((c) => c.textContent ?? "");
}

function panelButton(text: string): HTMLButtonElement | undefined {
  return [...(document.querySelector("section[aria-labelledby]")?.querySelectorAll("button") ?? [])].find(
    (b) => b.textContent?.trim() === text,
  );
}

function buttonByText(text: string): HTMLButtonElement | undefined {
  return [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
}

function mainCommand(): string {
  return document.querySelector('code[aria-label="安装命令"]')?.textContent ?? "";
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("oneLineInstallCommand construction", () => {
  it("downloads install-device.sh from the base URL with curl and passes the real --server/--code/--version values", () => {
    expect(oneLineInstallCommand("https://nexterm.example.com", false, "c-1", "0.2.2")).toBe(
      "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
        "--server 'https://nexterm.example.com' --code 'c-1' --version '0.2.2'",
    );
  });

  it("strictly validates certificates on both sides for non-insecure base URLs and never emits --insecure", () => {
    const command = oneLineInstallCommand("https://nexterm.example.com", false, "c-1", "0.2.2");
    expect(command).toBe(
      "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
        "--server 'https://nexterm.example.com' --code 'c-1' --version '0.2.2'",
    );
    expect(command).not.toContain("--insecure");
  });

  it("relaxes certificate checks in both curl and script/enroll arguments for an explicitly insecure self-signed HTTPS base URL", () => {
    const command = oneLineInstallCommand("https://10.0.0.8:8443", true, "c-1", "0.2.2");
    expect(command).toBe(
      "curl -fsSL --insecure 'https://10.0.0.8:8443/install-device.sh' | sh -s -- " +
        "--server 'https://10.0.0.8:8443' --code 'c-1' --version '0.2.2' --insecure",
    );
  });

  it("single-quotes shell metacharacters in dynamic arguments and escapes embedded single quotes", () => {
    const command = oneLineInstallCommand("https://nexterm.example.com/mirror;v2/$(id)", false, "co'de", "0.2.2");
    expect(command).toContain("'https://nexterm.example.com/mirror;v2/$(id)/install-device.sh'");
    expect(command).toContain("--code 'co'\\''de'");
    // The constructed result contains no unwrapped metacharacters overall (each argument remains single-quote safe)
    expect(shellQuote("a'b")).toBe("'a'\\''b'");
  });
});

describe("oneLineInstallCommandWindows construction", () => {
  it("downloads install-device.ps1 with iex and forwards the same values as PowerShell-native parameters", () => {
    expect(oneLineInstallCommandWindows("https://nexterm.example.com", false, "c-1", "0.2.2")).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://nexterm.example.com/install-device.ps1') } " +
        "-Server 'https://nexterm.example.com' -Code 'c-1' -Version '0.2.2'\"",
    );
  });

  it("passes -Insecure through to the script arguments for an explicitly insecure base URL", () => {
    const command = oneLineInstallCommandWindows("https://10.0.0.8:8443", true, "c-1", "0.2.2");
    expect(command).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://10.0.0.8:8443/install-device.ps1') } " +
        "-Server 'https://10.0.0.8:8443' -Code 'c-1' -Version '0.2.2' -Insecure\"",
    );
  });

  it("quotes dynamic arguments as PowerShell literal strings and doubles embedded single quotes", () => {
    const command = oneLineInstallCommandWindows("https://nexterm.example.com/mirror;v2/$(id)", false, "co'de", "0.2.2");
    expect(command).toContain("'https://nexterm.example.com/mirror;v2/$(id)/install-device.ps1'");
    expect(command).toContain("-Code 'co''de'");
    expect(psQuote("a'b")).toBe("'a''b'");
  });
});

describe("releaseOrNull", () => {
  it("accepts release versions only", () => {
    expect(releaseOrNull("1.3.1")).toBe("1.3.1");
    expect(releaseOrNull("0.2.2-rc.7")).toBe("0.2.2-rc.7");
    for (const v of ["dev", "", "0.0.0", "v1.3.1", "1.3", "1.3.1+abc", null, undefined]) expect(releaseOrNull(v)).toBeNull();
  });
});

describe("Device management - guided enrollment", () => {
  it("shows one command for the default platform with a single copy button, and the raw code stays hidden", async () => {
    const calls = route();
    await openIssuedPanel();

    expect(calls.some((c) => c.url === "/healthz")).toBe(true);
    expect(mainCommand()).toBe(
      "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
        "--server 'https://nexterm.example.com' --code 'fleet-code-1' --version '0.2.2'",
    );
    // Platform defaults to macOS / Linux (jsdom is not a Windows host) and only that command is shown.
    expect(document.querySelector('button[aria-pressed="true"]')?.textContent).toBe("macOS / Linux");
    expect(mainCommand()).not.toContain("install-device.ps1");
    expect([...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "复制命令")).toHaveLength(1);
    expect(document.body.textContent).toContain("在目标设备上用普通用户运行，无需管理员权限");
    expect(document.body.textContent).toContain("接入码仅可使用一次，设备凭证只保存在设备上。");
    expect(document.body.textContent).not.toContain("本页面不会执行安装");

    // The raw code is not displayed on its own until the manual disclosure is opened.
    expect(commandTexts().some((c) => c === "fleet-code-1")).toBe(false);
    expect(document.querySelector('button[aria-label="复制接入码"]')).toBeNull();
    const manual = [...document.querySelectorAll("details")].find((d) => d.querySelector("summary")?.textContent === "手动接入");
    expect(manual?.open).toBe(false);
    act(() => {
      if (manual) manual.open = true;
      manual?.dispatchEvent(new Event("toggle"));
    });
    expect(commandTexts().some((c) => c === "fleet-code-1")).toBe(true);
    expect(commandTexts().some((c) => c.includes("nexterm-agent enroll"))).toBe(true);
    expect(commandTexts().some((c) => c.includes("nexterm-agent install"))).toBe(true);
  });

  it("switches to the PowerShell command with PowerShell-native parameters and states the Windows requirements", async () => {
    route();
    await openIssuedPanel();
    click(buttonByText("Windows") as HTMLButtonElement);
    expect(mainCommand()).toBe(
      "iex \"& { $(iwr -UseBasicParsing 'https://nexterm.example.com/install-device.ps1') } " +
        "-Server 'https://nexterm.example.com' -Code 'fleet-code-1' -Version '0.2.2'\"",
    );
    expect(mainCommand()).not.toContain("install-device.sh");
    expect(document.body.textContent).toContain("无需管理员权限");
    expect(document.body.textContent).toContain("PowerShell");
  });

  it("keeps fallback addresses inside the more-options disclosure", async () => {
    route();
    await openIssuedPanel();
    const more = [...document.querySelectorAll("details")].find((d) => d.querySelector("summary")?.textContent === "更多选项");
    expect(more?.open).toBe(false);
    const fallback = [...more!.querySelectorAll("code")].map((c) => c.textContent ?? "");
    expect(fallback).toContain(
      "curl -fsSL --insecure 'https://10.0.0.8:8443/install-device.sh' | sh -s -- " +
        "--server 'https://10.0.0.8:8443' --code 'fleet-code-1' --version '0.2.2' --insecure",
    );
    expect(fallback.some((c) => c.includes("install-device.ps1") && c.includes("-Insecure"))).toBe(true);
  });

  it("keeps single-quote safety when a base URL contains shell metacharacters", async () => {
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      const ok = (payload: unknown) => ({ ok: true, status: 200, text: async () => JSON.stringify(payload) });
      if (u === "/fleet/devices" && method === "GET") return ok({ devices: [AGENT_DEVICE] });
      if (u === "/fleet/base-urls" && method === "GET") return ok({ base_urls: [{ url: "https://nexterm.example.com/mirror;v2/$(id)" }] });
      if (u === "/healthz" && method === "GET") return ok({ ok: true, version: "0.2.2" });
      if (u === "/device/enroll-codes" && method === "POST") return ok({ code: "fleet-code-1", expires_at: Date.now() + 900_000 });
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    await openIssuedPanel();
    expect(mainCommand()).toContain("curl -fsSL 'https://nexterm.example.com/mirror;v2/$(id)/install-device.sh'");
    expect(mainCommand()).toContain("--server 'https://nexterm.example.com/mirror;v2/$(id)'");
  });

  it("switches the copy button to the copied state for two seconds", async () => {
    vi.useFakeTimers();
    route();
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText, readText: async () => "" } });
    await openIssuedPanel();
    click(buttonByText("复制命令") as HTMLButtonElement);
    await advance(0);
    expect(writeText).toHaveBeenCalledWith(mainCommand());
    expect(buttonByText("已复制")).toBeTruthy();
    await advance(1900);
    expect(buttonByText("已复制")).toBeTruthy();
    await advance(200);
    expect(buttonByText("已复制")).toBeUndefined();
    expect(buttonByText("复制命令")).toBeTruthy();
  });

  it("shows the waiting state with a live status region and the remaining validity", async () => {
    route();
    await openIssuedPanel();
    const status = [...document.querySelectorAll('[role="status"]')].find((e) => e.textContent?.includes("等待设备接入"));
    expect(status?.getAttribute("aria-live")).toBe("polite");
    expect(document.body.textContent).toContain("接入码 15 分钟后失效");
    expect(panelButton("打开工作区")).toBeUndefined();
  });

  it("polls every 3 seconds and switches to the connected state when a new device appears", async () => {
    vi.useFakeTimers();
    const calls = route();
    await openIssuedPanel();
    const listCalls = () => calls.filter((c) => c.url === "/fleet/devices").length;
    const before = listCalls();

    await advance(3000);
    expect(listCalls()).toBeGreaterThan(before);
    expect(document.body.textContent).toContain("等待设备接入");

    scenario.devices = [AGENT_DEVICE, { ...NEW_DEVICE, last_seen_at: Date.now(), agent: { ...NEW_DEVICE.agent, last_seen_at: Date.now() } }];
    await advance(3000);
    await advance(0);
    expect(document.body.textContent).toContain("已接入：new-host");
    expect(document.body.textContent).not.toContain("等待设备接入");
    expect(panelButton("打开工作区")?.disabled).toBe(false);
    expect(buttonByText("再接入一台")).toBeTruthy();
    expect(commandTexts().some((c) => c.includes("install-device"))).toBe(false);

    const afterJoin = listCalls();
    await advance(9000);
    expect(listCalls()).toBe(afterJoin);
  });

  it("generates a new code with 再接入一台 and starts waiting again from the new baseline", async () => {
    vi.useFakeTimers();
    route();
    await openIssuedPanel();
    scenario.devices = [AGENT_DEVICE, { ...NEW_DEVICE, last_seen_at: Date.now(), agent: { ...NEW_DEVICE.agent, last_seen_at: Date.now() } }];
    await advance(3000);
    await advance(0);
    expect(document.body.textContent).toContain("已接入：new-host");
    click(buttonByText("再接入一台") as HTMLButtonElement);
    await advance(0);
    await advance(0);
    expect(scenario.issued).toBe(2);
    expect(document.body.textContent).toContain("等待设备接入");
    expect(mainCommand()).toContain("'fleet-code-2'");
  });

  it("shows the expired state and regenerates a fresh code", async () => {
    vi.useFakeTimers();
    scenario.ttlMs = 60_000;
    route();
    await openIssuedPanel();
    expect(document.body.textContent).toContain("接入码不足 1 分钟后失效");

    await advance(65_000);
    expect(document.body.textContent).toContain("接入码已失效");
    expect(commandTexts().some((c) => c.includes("install-device"))).toBe(false);
    expect(document.body.textContent).not.toContain("等待设备接入");

    scenario.ttlMs = 900_000;
    click(buttonByText("重新生成") as HTMLButtonElement);
    await advance(0);
    await advance(0);
    expect(scenario.issued).toBe(2);
    expect(document.body.textContent).toContain("等待设备接入");
    expect(document.body.textContent).toContain("接入码 15 分钟后失效");
    expect(mainCommand()).toContain("'fleet-code-2'");
  });

  it("does not poll while the page is hidden", async () => {
    vi.useFakeTimers();
    const calls = route();
    await openIssuedPanel();
    const hidden = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    try {
      const before = calls.filter((c) => c.url === "/fleet/devices").length;
      await advance(9000);
      expect(calls.filter((c) => c.url === "/fleet/devices").length).toBe(before);
    } finally {
      hidden.mockRestore();
    }
  });

  it("moves focus to the panel heading when opened and back to the opener when closed", async () => {
    route();
    await openIssuedPanel();
    expect(document.activeElement?.id).toBe("enroll-panel-title");
    click(buttonByText("关闭") as HTMLButtonElement);
    expect(document.activeElement?.textContent).toContain("接入新设备");
  });

  it("hides the one-click commands when /healthz fails and explains the manual path instead of fabricating a version", async () => {
    healthBehavior.mode = "fail";
    route();
    await openIssuedPanel();
    expect(commandTexts().some((c) => c.includes("install-device"))).toBe(false);
    expect(document.body.textContent).not.toContain("--version");
    expect(document.body.textContent).toContain("无法读取服务器版本");
    expect(commandTexts().some((c) => c.includes("nexterm-agent enroll"))).toBe(true);
  });

  for (const mode of ["empty", "dev"] as const) {
    it(`treats a ${mode} server version as unavailable for one-click commands`, async () => {
      healthBehavior.mode = mode;
      route();
      await openIssuedPanel();
      expect(commandTexts().some((c) => c.includes("install-device"))).toBe(false);
      expect(document.body.textContent).toContain("无法读取服务器版本");
    });
  }

  it("adds the old-agent hint for a newly connected device whose agent is older than the server", async () => {
    vi.useFakeTimers();
    mocks.fetch.mockReset();
    route();
    await openIssuedPanel();
    const old = { ...NEW_DEVICE, last_seen_at: Date.now(), agent: { ...NEW_DEVICE.agent, app_version: "0.1.0", last_seen_at: Date.now() } };
    scenario.devices = [AGENT_DEVICE, old];
    await advance(3000);
    await advance(0);
    const panel = document.querySelector("section[aria-labelledby]");
    expect(panel?.textContent).toContain("设备代理版本较旧");
    expect(panel?.textContent).toContain("nexterm-agent update");
  });
});

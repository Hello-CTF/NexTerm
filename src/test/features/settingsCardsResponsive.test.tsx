/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    memorySettings: vi.fn(),
    memoryIndex: vi.fn(),
    memoryGet: vi.fn(),
    memorySetSettings: vi.fn(),
    knownHostList: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/memory", () => ({
  memoryApi: {
    settings: mocks.memorySettings,
    index: mocks.memoryIndex,
    get: mocks.memoryGet,
    create: vi.fn(),
    edit: vi.fn(),
    delete: vi.fn(),
    setSettings: mocks.memorySetSettings,
  },
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      knownHostList: mocks.knownHostList,
      knownHostRemove: vi.fn(),
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { MemoryCard } from "../../features/settings/MemoryCard";
import { KnownHostsCard } from "../../features/settings/KnownHostsCard";
import { useUi } from "../../app/store";
import type { MemoryTopicIndex } from "../../ipc/memory";

const ULID = "01J4ZQXK6M2N8P3R5T7V9W0Y1Z";
const LONG_TOKEN = `tok_${"a1B2".repeat(24)}`;

const INDEX: MemoryTopicIndex[] = [
  {
    topic: "operations-long-topic-name-for-narrow-layout",
    entries: [{ id: ULID, version: 3, redacted: true, updatedAt: 1728000000000 }],
  },
];

const KNOWN_HOST = {
  id: "kh1",
  host: "r120-audit-host-with-a-very-long-hostname.example-internal.company.com",
  port: 2222,
  keyType: "ssh-ed25519",
  fingerprint: `SHA256:${"b".repeat(43)}`,
  addedAt: 1728000000000,
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.memorySettings.mockResolvedValue({ injectionEnabled: false, toolsEnabled: false, version: 3 });
  mocks.memoryIndex.mockResolvedValue(INDEX);
  mocks.memoryGet.mockResolvedValue({
    id: ULID,
    topic: "operations",
    content: `Restart scheduled for 02:00; token ${LONG_TOKEN} must not overflow without wrapping`,
    version: 3,
    redacted: true,
    createdAt: 1,
    updatedAt: 2,
  });
  mocks.knownHostList.mockResolvedValue([KNOWN_HOST]);
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("MemoryCard narrow-screen layout", () => {
  it("truncates the ULID while preserving full-value access and allowing rows to wrap", async () => {
    mounted = mount(createElement(MemoryCard));
    await flushUntil(() => mounted!.container.textContent?.includes(ULID) ?? false);
    const idSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === ULID,
    );
    expect(idSpan).toBeTruthy();
    expect(idSpan!.className).toContain("truncate");
    expect(idSpan!.getAttribute("title")).toBe(ULID);
    const row = idSpan!.closest("button");
    expect(row?.className).toContain("flex-wrap");
    expect(row?.className).toContain("min-w-0");
    expect(mounted.container.textContent).toContain("已脱敏");
    expect(mounted.container.textContent).toContain("v3 ·");
  });

  it("wraps long tokens in expanded pre content", async () => {
    mounted = mount(createElement(MemoryCard));
    await flushUntil(() => mounted!.container.textContent?.includes(ULID) ?? false);
    const row = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes(ULID),
    );
    click(row!);
    await flushUntil(() => mounted!.container.querySelector("pre") !== null);
    const pre = mounted.container.querySelector("pre");
    expect(pre?.className).toContain("break-words");
    expect(pre?.textContent).toContain(LONG_TOKEN);
  });

  it("makes both toggle rows full labels so clicking their text toggles them", async () => {
    mocks.memorySetSettings.mockResolvedValue({ injectionEnabled: true, toolsEnabled: false, version: 4 });
    mounted = mount(createElement(MemoryCard));
    await flushUntil(() => {
      const input = mounted!.container.querySelector<HTMLInputElement>(
        'input[aria-label="在 AI 运行时注入记忆"]',
      );
      return !!input && !input.disabled;
    });
    for (const name of ["在 AI 运行时注入记忆", "允许模型使用记忆工具"]) {
      const input = mounted.container.querySelector<HTMLInputElement>(
        `input[aria-label="${name}"]`,
      );
      expect(input?.closest("label")).toBeTruthy();
    }
    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="在 AI 运行时注入记忆"]')!;
    click(input.closest("label")!);
    await act(async () => {
      await flush();
    });
    expect(mocks.memorySetSettings).toHaveBeenCalledWith(3, { injectionEnabled: true });
    const updated = mounted.container.querySelector<HTMLInputElement>('input[aria-label="在 AI 运行时注入记忆"]')!;
    expect(updated.checked).toBe(true);
    expect(updated.disabled).toBe(false);
  });
});

describe("KnownHostsCard narrow-screen layout", () => {
  it("truncates long hostnames with the full value in the title and keeps keyType and revoke actions visible", async () => {
    mounted = mount(createElement(KnownHostsCard));
    await flushUntil(() => mounted!.container.textContent?.includes(KNOWN_HOST.host) ?? false);
    const hostSpan = [...mounted.container.querySelectorAll("span")].find((s) =>
      s.textContent?.includes(KNOWN_HOST.host),
    );
    expect(hostSpan).toBeTruthy();
    expect(hostSpan!.className).toContain("truncate");
    expect(hostSpan!.getAttribute("title")).toBe(`${KNOWN_HOST.host}:${KNOWN_HOST.port}`);
    expect(mounted.container.textContent).toContain("ssh-ed25519");
    const fp = [...mounted.container.querySelectorAll("div")].find((d) =>
      d.textContent === KNOWN_HOST.fingerprint,
    );
    expect(fp?.className).toContain("truncate");
    expect(fp?.getAttribute("title")).toBe(KNOWN_HOST.fingerprint);
    const revoke = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("撤销信任"),
    );
    expect(revoke).toBeTruthy();
    expect(revoke!.className).toContain("shrink-0");
  });
});

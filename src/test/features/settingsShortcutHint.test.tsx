/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { mac: false };
});

vi.mock("../../app/platform", () => ({
  isMac: () => harness.mac,
  modHint: () => (harness.mac ? "⌘" : "Ctrl"),
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  syncApi: {},
  vaultApi: {
    status: vi.fn().mockResolvedValue({ initialized: true, unlocked: true }),
  },
  aiApi: {
    getPermission: vi.fn().mockResolvedValue({ mode: "read_write", dangerRules: [] }),
    setPermission: vi.fn(),
    testProvider: vi.fn(),
  },
  filesApi: {
    settingsGet: vi.fn().mockResolvedValue({ publicBaseURL: "" }),
    settingsSet: vi.fn(),
  },
}));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return { ...actual, WEB: true, TRANSPORT: "web" as const, DESKTOP: false };
});

vi.mock("../../features/settings/AppearanceCard", () => ({ AppearanceCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/CronCard", () => ({ CronCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/settings/SyncBundleCard", () => ({ SyncBundleCard: () => null }));
vi.mock("../../features/settings/ShareCard", () => ({ ShareCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";

let mounted: MountedView | undefined;
let client: QueryClient | undefined;

async function show(): Promise<void> {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(SettingsView)),
  );
  await flush();
}

function shortcutRows(): Array<[string, string]> {
  return [...document.querySelectorAll<HTMLElement>(".nx-kbd")].map((kbd) => [
    kbd.textContent ?? "",
    kbd.nextElementSibling?.textContent ?? "",
  ]);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  harness.mac = false;
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  client?.clear();
});

describe("settings shortcut list platform hint", () => {
  it("advertises Ctrl+F for terminal search on non-mac platforms", async () => {
    await show();
    expect(shortcutRows()).toContainEqual(["Ctrl+F", "终端内搜索"]);
  });

  it("advertises ⌘F for terminal search on macOS", async () => {
    harness.mac = true;
    await show();
    const rows = shortcutRows();
    expect(rows).toContainEqual(["⌘+F", "终端内搜索"]);
    expect(rows).not.toContainEqual(["Ctrl+F", "终端内搜索"]);
  });
});

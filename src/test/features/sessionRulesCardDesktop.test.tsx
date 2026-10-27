/** @vitest-environment jsdom */
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clickButton, flush, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    list: vi.fn(),
    status: vi.fn(),
    exportRules: vi.fn(),
    importRules: vi.fn(),
    assetList: vi.fn(),
    pickSavePath: vi.fn(),
    pickLocalFile: vi.fn(),
    saveFile: vi.fn(),
    readFile: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { list: mocks.assetList },
    terminalApi: { snapshot: vi.fn() },
    localFileApi: { read: mocks.readFile },
  };
});
vi.mock("../../ipc/logTailApi", () => ({ logTailApi: { save: mocks.saveFile } }));
vi.mock("../../ipc/stateRuleApi", () => ({
  stateRuleApi: { list: mocks.list, status: mocks.status, export: mocks.exportRules, import: mocks.importRules, validate: vi.fn() },
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, pickSavePath: mocks.pickSavePath, pickLocalFile: mocks.pickLocalFile };
});

import { SessionRulesCard } from "../../features/settings/SessionRulesCard";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.list.mockResolvedValue([{ id: "R1", name: "review", scope: "global", region: "bottom", lines: 5, pattern: "review", state: "needs_me", enabled: true, updatedAt: 1 }]);
  mocks.status.mockResolvedValue([]);
  mocks.assetList.mockResolvedValue([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("state rules export and import on desktop", () => {
  it("writes the exported file to the chosen path and reads an import from a picked file", async () => {
    const exported = { format: "nexterm.state-rules", version: 1, rules: [{ name: "review", scope: "global", region: "bottom", lines: 5, pattern: "review", state: "needs_me", enabled: true }] };
    mocks.exportRules.mockResolvedValue(exported);
    mocks.pickSavePath.mockResolvedValue("/home/me/rules.json");
    mocks.saveFile.mockResolvedValue({ path: "/home/me/rules.json", bytes: 1 });
    mounted = mountWithClient(createElement(SessionRulesCard));
    await flush();

    clickButton(mounted.container, "导出");
    await flush();
    expect(mocks.pickSavePath).toHaveBeenCalledWith("nexterm-state-rules.json");
    const [path, text] = mocks.saveFile.mock.calls[0] as [string, string];
    expect(path).toBe("/home/me/rules.json");
    expect(JSON.parse(text)).toEqual(exported);

    mocks.pickLocalFile.mockResolvedValue("/home/me/rules.json");
    mocks.readFile.mockResolvedValue({ path: "/home/me/rules.json", size: text.length, contentBase64: btoa(text) });
    mocks.importRules.mockResolvedValue({ created: 1, updated: 0, unchanged: 0 });
    clickButton(mounted.container, "导入");
    await flush();
    await flush();
    expect(mocks.importRules).toHaveBeenCalledWith(exported);
  });

  it("does nothing when the save dialog is cancelled", async () => {
    mocks.exportRules.mockResolvedValue({ format: "nexterm.state-rules", version: 1, rules: [] });
    mocks.pickSavePath.mockResolvedValue(null);
    mounted = mountWithClient(createElement(SessionRulesCard));
    await flush();
    clickButton(mounted.container, "导出");
    await flush();
    expect(mocks.saveFile).not.toHaveBeenCalled();
  });
});

/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ connectivityTest: vi.fn(), confirmDialog: vi.fn() }));

vi.mock("../../ipc/hostPortsApi", () => ({ hostPortsApi: { list: vi.fn(), connectivityTest: mocks.connectivityTest } }));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, confirmDialog: mocks.confirmDialog };
});

import { ConnectivityTest } from "../../features/hosts/ConnectivityTest";

let mounted: MountedView | undefined;

function mountTest() {
  mounted = mountWithClient(createElement(ConnectivityTest, { sessionId: "s1", hostName: "web-01" }));
  const root = mounted.container;
  setInputValue(root.querySelector<HTMLInputElement>('input[aria-label="目标主机"]')!, "db.internal");
  setInputValue(root.querySelector<HTMLInputElement>('input[aria-label="目标端口"]')!, "5432");
  return root;
}

const testButton = (root: HTMLElement) =>
  [...root.querySelectorAll("button")].find((b) => b.textContent === "测试")!;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.connectivityTest.mockImplementation(async (args: { dryRun?: boolean }) =>
    args.dryRun
      ? { command: "nc -z db.internal 5432", ran: false, ok: false, output: "", durationMs: 0 }
      : { command: "nc -z db.internal 5432", ran: true, ok: true, exitCode: 0, output: "<b>open</b>", durationMs: 12 },
  );
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("ConnectivityTest", () => {
  it("previews the exact command before running and shows the output as plain text", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    const root = mountTest();
    click(testButton(root));
    await flushUntil(() => root.querySelector('[data-testid="connectivity-result"]') !== null);
    expect(mocks.connectivityTest.mock.calls[0][0]).toMatchObject({ method: "tcp", host: "db.internal", port: 5432, dryRun: true });
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain("nc -z db.internal 5432");
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain("web-01");
    expect(mocks.connectivityTest.mock.calls[1][0].dryRun).toBeFalsy();
    const pre = root.querySelector("pre")!;
    expect(pre.textContent).toBe("<b>open</b>");
    expect(pre.querySelector("b")).toBeNull();
  });

  it("does not run when the confirmation is cancelled", async () => {
    mocks.confirmDialog.mockResolvedValue(false);
    const root = mountTest();
    click(testButton(root));
    await flushUntil(() => mocks.confirmDialog.mock.calls.length > 0);
    await Promise.resolve();
    expect(mocks.connectivityTest).toHaveBeenCalledTimes(1);
    expect(root.querySelector('[data-testid="connectivity-result"]')).toBeNull();
  });

  it("shows the backend's validation error", async () => {
    mocks.connectivityTest.mockRejectedValue(new Error("端口不合法"));
    const root = mountTest();
    click(testButton(root));
    await flushUntil(() => root.querySelector('[role="alert"]') !== null);
    expect(root.querySelector('[role="alert"]')!.textContent).toContain("端口不合法");
    expect(mocks.confirmDialog).not.toHaveBeenCalled();
  });
});

/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  sessionApi: {},
  assetApi: {},
  dbApi: {},
  terminalApi: {},
  vaultApi: {},
}));

async function restartStore() {
  vi.resetModules();
  return (await import("../../app/store")).useUi;
}

beforeEach(() => {
  localStorage.clear();
});

describe("AI panel startup preference", () => {
  it("keeps a closed panel closed immediately after restarting", async () => {
    const store = await restartStore();
    store.getState().setRightOpen(false);

    const restarted = await restartStore();

    expect(restarted.getState().rightOpen).toBe(false);
    restarted.getState().setRightOpen(true);
    expect((await restartStore()).getState().rightOpen).toBe(true);
  });

  it("caches remotely applied preferences for the next startup", async () => {
    const store = await restartStore();
    store.setState({ rightOpen: false, leftWidth: 280, rightWidth: 420 });

    const restarted = await restartStore();

    expect(restarted.getState()).toMatchObject({ rightOpen: false, leftWidth: 280, rightWidth: 420 });
    restarted.getState().setRightWidth(460);
    expect((await restartStore()).getState()).toMatchObject({ rightOpen: false, rightWidth: 460 });
  });

  it("preserves the default for existing width-only preferences", async () => {
    localStorage.setItem("nexterm.layout.v1", JSON.stringify({ leftWidth: 280, rightWidth: 420 }));

    expect((await restartStore()).getState()).toMatchObject({ rightOpen: true, leftWidth: 280, rightWidth: 420 });
  });
});

import { beforeEach, describe, expect, it, vi } from "vitest";

const call = vi.hoisted(() => vi.fn());
vi.mock("../ipc/commands", () => ({ call }));

import { diagnosticsApi } from "../ipc/diagnosticsApi";

describe("diagnosticsApi", () => {
  beforeEach(() => {
    call.mockReset();
    call.mockResolvedValue({ ok: true });
  });

  it("maps each action to its IPC command and arguments", async () => {
    await diagnosticsApi.status();
    await diagnosticsApi.arm(true);
    await diagnosticsApi.openFolder();
    await diagnosticsApi.openFolder("/captures/a");
    await diagnosticsApi.pack("/captures/a");
    await diagnosticsApi.remove("/captures/a");
    await diagnosticsApi.clear();
    await diagnosticsApi.reportFrontend({ kind: "window", fps: 60 });

    expect(call.mock.calls).toEqual([
      ["diagnostics_status"],
      ["diagnostics_arm", { armed: true }],
      ["diagnostics_open_folder", { path: "" }],
      ["diagnostics_open_folder", { path: "/captures/a" }],
      ["diagnostics_pack", { path: "/captures/a" }],
      ["diagnostics_delete", { path: "/captures/a" }],
      ["diagnostics_clear"],
      ["diagnostics_report_frontend", { kind: "window", fps: 60 }],
    ]);
  });

  it("returns the command result", async () => {
    call.mockResolvedValueOnce({ path: "/captures/a.zip" });
    await expect(diagnosticsApi.pack("/captures/a")).resolves.toEqual({ path: "/captures/a.zip" });
  });
});

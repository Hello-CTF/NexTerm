/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  openExternalURL: vi.fn(),
  ask: vi.fn(),
  list: vi.fn(),
  pushToast: vi.fn(),
  addTab: vi.fn(),
  localHostname: vi.fn(),
}));

async function load(web: boolean) {
  vi.resetModules();
  vi.doMock("../../ipc/env", () => ({ WEB: web }));
  vi.doMock("../../ipc/wails", () => ({ openExternalURL: mocks.openExternalURL }));
  vi.doMock("../../ui/dialogs", () => ({ ask: mocks.ask }));
  vi.doMock("../../ipc/commands", () => ({
    fsApi: { list: mocks.list },
    sessionApi: { localHostname: mocks.localHostname },
  }));
  const { useUi } = await import("../../app/store");
  useUi.setState({
    pushToast: mocks.pushToast,
    addTab: mocks.addTab,
    sessions: [
      { id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
      { id: "local-1", assetId: null, name: "本机", kind: "local", status: "connected", tabs: [], createdAt: 0 },
    ],
  });
  return import("../../features/terminal/terminalLinkActions");
}

afterEach(() => {
  vi.doUnmock("../../ipc/env");
  vi.doUnmock("../../ipc/wails");
  vi.doUnmock("../../ui/dialogs");
  vi.doUnmock("../../ipc/commands");
  vi.restoreAllMocks();
  for (const mock of Object.values(mocks)) mock.mockReset();
});

describe("terminal link actions", () => {
  it("opens external links in a noopener, noreferrer tab on the web", async () => {
    const { terminalLinkDeps } = await load(true);
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    await terminalLinkDeps("s1", undefined, () => null).openExternal("https://example.com/");
    expect(open).toHaveBeenCalledWith("https://example.com/", "_blank", "noopener,noreferrer");
    expect(mocks.openExternalURL).not.toHaveBeenCalled();
  });

  it("hands external links to the system on desktop", async () => {
    const { terminalLinkDeps } = await load(false);
    const open = vi.spyOn(window, "open");
    mocks.openExternalURL.mockResolvedValue(undefined);
    await terminalLinkDeps("s1", undefined, () => null).openExternal("mailto:ops@example.com");
    expect(mocks.openExternalURL).toHaveBeenCalledWith("mailto:ops@example.com");
    expect(open).not.toHaveBeenCalled();
  });

  it("confirms through the in-app dialog, not window.confirm", async () => {
    const { terminalLinkDeps } = await load(false);
    const confirm = vi.spyOn(window, "confirm");
    mocks.ask.mockResolvedValue(true);
    await expect(terminalLinkDeps("s1", undefined, () => null).confirm("open?", "打开外部链接")).resolves.toBe(true);
    expect(mocks.ask).toHaveBeenCalledWith("open?", { title: "打开外部链接", kind: "warning" });
    expect(confirm).not.toHaveBeenCalled();
  });

  it("opens session files as editor tabs and points directories at the sidebar file tree", async () => {
    const { terminalLinkDeps } = await load(false);
    const { openTerminalLink } = await import("../../features/terminal/terminalLinks");
    mocks.list.mockResolvedValue([
      { name: "app.log.txt", path: "/var/log/app.log.txt", kind: "file" },
      { name: "nginx", path: "/var/log/nginx", kind: "dir" },
    ]);
    const deps = terminalLinkDeps("s1", undefined, () => "web01");
    await openTerminalLink("file://web01/var/log/app.log.txt", deps);
    await openTerminalLink("file://web01/var/log/nginx", deps);
    expect(mocks.list).toHaveBeenCalledWith("s1", "/var/log");
    expect(mocks.addTab).toHaveBeenNthCalledWith(1, expect.objectContaining({ kind: "files", sessionId: "s1", path: "/var/log/app.log.txt" }));
    // No file browser tab: a directory only produces a hint toward the sidebar tree.
    expect(mocks.addTab).toHaveBeenCalledTimes(1);
    expect(mocks.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("/var/log/nginx"));
  });

  it("matches local sessions against the backend machine name, not the shell's reported host", async () => {
    const { terminalLinkDeps } = await load(false);
    mocks.localHostname.mockResolvedValue("laptop");
    // A nested ssh reported its host first: it must not count for a local session.
    const local = terminalLinkDeps("local-1", undefined, () => "nested-remote");
    await expect(local.sessionHost()).resolves.toBe("laptop");
    await expect(local.sessionHost()).resolves.toBe("laptop");
    expect(mocks.localHostname).toHaveBeenCalledTimes(1);
    expect(mocks.localHostname).toHaveBeenCalledWith("local-1");

    // Remote sessions keep using the host their shell reported.
    expect(terminalLinkDeps("s1", undefined, () => "web01").sessionHost()).toBe("web01");
  });

  it("treats the local host as unknown when the backend cannot name it, and retries later", async () => {
    const { terminalLinkDeps } = await load(false);
    mocks.localHostname.mockRejectedValueOnce(new Error("unsupported")).mockResolvedValueOnce("laptop");
    const local = terminalLinkDeps("local-1", undefined, () => null);
    await expect(local.sessionHost()).resolves.toBeNull();
    await expect(local.sessionHost()).resolves.toBe("laptop");
  });
});

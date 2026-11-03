import { describe, expect, it, vi } from "vitest";
import { openTerminalLink, type TerminalLinkDeps } from "../../features/terminal/terminalLinks";

type Mode = "system" | "app" | "ask";

function setup(mode: Mode | undefined, choice: "system" | "app" | null = null, withOpenInApp = true) {
  const openExternal = vi.fn();
  const openInApp = vi.fn();
  const chooseLinkTarget = vi.fn().mockResolvedValue(choice);
  const deps = {
    sessionId: "s1",
    sessionHost: () => null,
    openExternal,
    confirm: vi.fn().mockResolvedValue(true),
    notify: vi.fn(),
    listDirectory: vi.fn(),
    isEditableFile: () => false,
    openFile: vi.fn(),
    openDirectory: vi.fn(),
    describeError: String,
    ...(mode ? { linkOpenMode: () => mode } : {}),
    ...(withOpenInApp ? { openInApp } : {}),
    chooseLinkTarget,
  } as unknown as TerminalLinkDeps;
  return { deps, openExternal, openInApp, chooseLinkTarget };
}

describe("terminal link open location", () => {
  it("system opens the system browser", async () => {
    const t = setup("system");
    await openTerminalLink("https://example.com/", t.deps);
    expect(t.openExternal).toHaveBeenCalledWith("https://example.com/");
    expect(t.openInApp).not.toHaveBeenCalled();
  });

  it("app opens inside NexTerm", async () => {
    const t = setup("app");
    await openTerminalLink("https://example.com/", t.deps);
    expect(t.openInApp).toHaveBeenCalledWith("https://example.com/");
    expect(t.openExternal).not.toHaveBeenCalled();
  });

  it("ask follows the choice", async () => {
    const app = setup("ask", "app");
    await openTerminalLink("https://example.com/", app.deps);
    expect(app.openInApp).toHaveBeenCalledWith("https://example.com/");
    expect(app.openExternal).not.toHaveBeenCalled();

    const sys = setup("ask", "system");
    await openTerminalLink("https://example.com/", sys.deps);
    expect(sys.openExternal).toHaveBeenCalledWith("https://example.com/");
    expect(sys.openInApp).not.toHaveBeenCalled();
  });

  it("ask cancelled opens nothing", async () => {
    const t = setup("ask", null);
    await openTerminalLink("https://example.com/", t.deps);
    expect(t.openExternal).not.toHaveBeenCalled();
    expect(t.openInApp).not.toHaveBeenCalled();
  });

  it("keeps the old behaviour without the new deps or without an in-app opener", async () => {
    const none = setup(undefined);
    await openTerminalLink("https://example.com/", none.deps);
    expect(none.openExternal).toHaveBeenCalledTimes(1);

    const noApp = setup("app", null, false);
    await openTerminalLink("https://example.com/", noApp.deps);
    expect(noApp.openExternal).toHaveBeenCalledTimes(1);
  });

  it("never routes mailto links in-app", async () => {
    const t = setup("app");
    await openTerminalLink("mailto:ops@example.com", t.deps);
    expect(t.openInApp).not.toHaveBeenCalled();
    expect(t.openExternal).toHaveBeenCalledTimes(1);
  });
});

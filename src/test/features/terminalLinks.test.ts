/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal, type IBufferRange } from "@xterm/xterm";
import { WebLinksAddon } from "@xterm/addon-web-links";
import {
  LINK_LONG_PRESS_MS,
  attachLinkLongPress,
  cleanTerminalLink,
  createTerminalLinkController,
  displayTerminalLink,
  fileLinkHostMatches,
  openTerminalLink,
  planTerminalLink,
  shouldOpenLink,
  type TerminalLinkActivation,
  type TerminalLinkDeps,
  type TerminalLinkEntry,
} from "../../features/terminal/terminalLinks";

describe("terminal link scheme policy", () => {
  it("opens http, https and mailto externally", () => {
    expect(planTerminalLink("https://example.com/a?b=1")).toEqual({ kind: "external", url: "https://example.com/a?b=1" });
    expect(planTerminalLink("HTTP://Example.com")).toEqual({ kind: "external", url: "http://example.com/" });
    expect(planTerminalLink("mailto:ops@example.com")).toEqual({ kind: "external", url: "mailto:ops@example.com" });
  });

  it("maps file links to a host and a decoded path", () => {
    expect(planTerminalLink("file://web01/home/me/My%20Notes.md")).toEqual({
      kind: "file",
      host: "web01",
      path: "/home/me/My Notes.md",
    });
    expect(planTerminalLink("file:///etc/hosts")).toEqual({ kind: "file", host: "", path: "/etc/hosts" });
    expect(planTerminalLink("file://localhost/tmp/")).toEqual({ kind: "file", host: "", path: "/tmp/" });
    expect(planTerminalLink("file:///C:/Users/me/a.txt")).toEqual({ kind: "file", host: "", path: "C:/Users/me/a.txt" });
  });

  it("rejects script and data schemes in any case", () => {
    for (const uri of ["javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,<b>x</b>", "vbscript:msgbox(1)"]) {
      expect(planTerminalLink(uri).kind, uri).toBe("blocked");
    }
  });

  it("rejects links carrying control or invisible characters and unparsable links", () => {
    expect(planTerminalLink("https://example.com/\x1b[31m").kind).toBe("blocked");
    expect(planTerminalLink("https://exa\u202emple.com").kind).toBe("blocked");
    expect(planTerminalLink("file:///tmp/%0Afoo").kind).toBe("blocked");
    expect(planTerminalLink("not a url").kind).toBe("blocked");
  });

  it("asks before handing other schemes to the system", () => {
    expect(planTerminalLink("ssh://root@example.com")).toEqual({ kind: "confirm", url: "ssh://root@example.com", scheme: "ssh" });
    expect(planTerminalLink("vscode://file/tmp/x")).toMatchObject({ kind: "confirm", scheme: "vscode" });
  });

  it("displays punycode hosts, decoded file paths and no control characters", () => {
    expect(displayTerminalLink("https://bücher.example/")).toBe("https://xn--bcher-kva.example/");
    expect(displayTerminalLink("file://web01/home/me/My%20Notes.md")).toBe("file://web01/home/me/My Notes.md");
    expect(displayTerminalLink("https://example.com/\x1b]0;x\x07")).not.toMatch(/[\x00-\x1f]/);
    expect(displayTerminalLink(`https://example.com/${"a".repeat(5000)}`).length).toBeLessThanOrEqual(1001);
    expect(cleanTerminalLink("https://ex\u200bample.com")).toBe("https://example.com");
  });
});

describe("file link host matching", () => {
  it("treats empty and localhost as the session's own host", () => {
    expect(fileLinkHostMatches("", null)).toBe(true);
    expect(fileLinkHostMatches("localhost", "web01")).toBe(true);
  });

  it("matches the reported host, including short name against FQDN", () => {
    expect(fileLinkHostMatches("web01", "web01")).toBe(true);
    expect(fileLinkHostMatches("WEB01", "web01.example.com")).toBe(true);
    expect(fileLinkHostMatches("web01.example.com", "web01")).toBe(true);
  });

  it("rejects other hosts and reports unknown when the session host is not known", () => {
    expect(fileLinkHostMatches("web02", "web01")).toBe(false);
    expect(fileLinkHostMatches("web01.a.com", "web01.b.com")).toBe(false);
    expect(fileLinkHostMatches("10.0.0.5", "10")).toBe(false);
    expect(fileLinkHostMatches("web02", null)).toBeNull();
  });
});

describe("modifier policy", () => {
  it("needs Ctrl off macOS and Cmd on macOS in modifier mode", () => {
    const plain = { ctrlKey: false, metaKey: false };
    expect(shouldOpenLink(plain, "modifier", false)).toBe(false);
    expect(shouldOpenLink({ ctrlKey: true, metaKey: false }, "modifier", false)).toBe(true);
    expect(shouldOpenLink({ ctrlKey: false, metaKey: true }, "modifier", false)).toBe(false);
    expect(shouldOpenLink({ ctrlKey: false, metaKey: true }, "modifier", true)).toBe(true);
    expect(shouldOpenLink({ ctrlKey: true, metaKey: false }, "modifier", true)).toBe(false);
    expect(shouldOpenLink(plain, "click", false)).toBe(true);
  });
});

function fakeDeps(overrides: Partial<TerminalLinkDeps> = {}) {
  const calls = {
    external: [] as string[],
    files: [] as string[],
    dirs: [] as string[],
    notices: [] as [string, string][],
    confirms: [] as string[],
    listed: [] as string[],
  };
  const entries: Record<string, TerminalLinkEntry[]> = {
    "/home/me": [
      { name: "notes.md", path: "/home/me/notes.md", kind: "file" },
      { name: "src", path: "/home/me/src", kind: "dir" },
      { name: "photo.png", path: "/home/me/photo.png", kind: "file" },
    ],
  };
  const deps: TerminalLinkDeps = {
    sessionId: "s1",
    sessionHost: () => "web01",
    openExternal: (url) => {
      calls.external.push(url);
    },
    confirm: async (message) => {
      calls.confirms.push(message);
      return false;
    },
    notify: (kind, text) => calls.notices.push([kind, text]),
    listDirectory: async (_id, dir) => {
      calls.listed.push(dir);
      if (!(dir in entries)) throw new Error("no such directory");
      return entries[dir];
    },
    isEditableFile: (name) => /\.(md|txt|ts)$/.test(name),
    openFile: (_id, path) => calls.files.push(path),
    openDirectory: (_id, path) => calls.dirs.push(path),
    describeError: (error) => (error instanceof Error ? error.message : String(error)),
    ...overrides,
  };
  return { deps, calls };
}

describe("opening terminal links", () => {
  it("opens web links externally and never opens dangerous schemes", async () => {
    const { deps, calls } = fakeDeps();
    await openTerminalLink("https://example.com/x", deps);
    await openTerminalLink("javascript:alert(document.cookie)", deps);
    await openTerminalLink("data:text/html,<script>1</script>", deps);
    expect(calls.external).toEqual(["https://example.com/x"]);
    expect(calls.notices.map(([kind]) => kind)).toEqual(["error", "error"]);
    expect(calls.notices[0][1]).toContain("javascript:");
  });

  it("requires in-app confirmation for other schemes", async () => {
    const declined = fakeDeps();
    await openTerminalLink("ssh://root@example.com", declined.deps);
    expect(declined.calls.confirms[0]).toContain("ssh://root@example.com");
    expect(declined.calls.external).toEqual([]);

    const accepted = fakeDeps({ confirm: async () => true });
    await openTerminalLink("ssh://root@example.com", accepted.deps);
    expect(accepted.calls.external).toEqual(["ssh://root@example.com"]);
  });

  it("reports a failed external open", async () => {
    const { deps, calls } = fakeDeps({ openExternal: () => Promise.reject(new Error("blocked by system")) });
    await openTerminalLink("https://example.com", deps);
    expect(calls.notices).toEqual([["error", expect.stringContaining("blocked by system")]]);
  });

  it("opens same-host files in the editor and directories in the file browser", async () => {
    const { deps, calls } = fakeDeps();
    await openTerminalLink("file://web01/home/me/notes.md", deps);
    await openTerminalLink("file://web01.example.com/home/me/src", deps);
    await openTerminalLink("file:///home/me/", deps);
    await openTerminalLink("file://localhost/", deps);
    expect(calls.files).toEqual(["/home/me/notes.md"]);
    expect(calls.dirs).toEqual(["/home/me/src", "/home/me/", "/"]);
    expect(calls.notices).toEqual([]);
  });

  it("shows other-host file links without opening anything", async () => {
    const { deps, calls } = fakeDeps();
    await openTerminalLink("file://web02/home/me/notes.md", deps);
    expect(calls.listed).toEqual([]);
    expect(calls.files).toEqual([]);
    expect(calls.notices).toEqual([["info", expect.stringContaining("file://web02/home/me/notes.md")]]);
  });

  it("asks before mapping a file link when the session host is unknown", async () => {
    const declined = fakeDeps({ sessionHost: () => null });
    await openTerminalLink("file://web01/home/me/notes.md", declined.deps);
    expect(declined.calls.confirms).toHaveLength(1);
    expect(declined.calls.files).toEqual([]);

    const accepted = fakeDeps({ sessionHost: () => null, confirm: async () => true });
    await openTerminalLink("file://web01/home/me/notes.md", accepted.deps);
    expect(accepted.calls.files).toEqual(["/home/me/notes.md"]);
    // Local and empty hosts need no confirmation.
    const local = fakeDeps({ sessionHost: () => null });
    await openTerminalLink("file:///home/me/notes.md", local.deps);
    await openTerminalLink("file://localhost/home/me/notes.md", local.deps);
    expect(local.calls.confirms).toEqual([]);
    expect(local.calls.files).toEqual(["/home/me/notes.md", "/home/me/notes.md"]);
  });

  it("explains files it cannot open", async () => {
    const { deps, calls } = fakeDeps();
    await openTerminalLink("file:///home/me/photo.png", deps);
    await openTerminalLink("file:///home/me/missing.txt", deps);
    await openTerminalLink("file:///nowhere/x.txt", deps);
    expect(calls.files).toEqual([]);
    expect(calls.notices.map(([kind]) => kind)).toEqual(["info", "error", "error"]);
    expect(calls.notices[2][1]).toContain("no such directory");
  });

  it("does not map file links from container terminals", async () => {
    const { deps, calls } = fakeDeps({ containerId: "c1" });
    await openTerminalLink("file:///home/me/notes.md", deps);
    expect(calls.listed).toEqual([]);
    expect(calls.notices).toEqual([["info", expect.stringContaining("file:///home/me/notes.md")]]);
  });
});

interface ProvidedLink {
  text: string;
  range: IBufferRange;
  activate: (event: MouseEvent, text: string) => void;
  hover?: (event: MouseEvent, text: string) => void;
  leave?: (event: MouseEvent, text: string) => void;
}

// xterm does not expose link lookup publicly: the tests ask its registered
// providers (OSC 8 and the web links addon) for the links on a line, the same
// way its mouse handling does.
function linksOnLine(term: Terminal, y: number): ProvidedLink[] {
  const core = (term as unknown as { _core: { _linkProviderService: { linkProviders: { provideLinks: (y: number, cb: (links: ProvidedLink[] | undefined) => void) => void }[] } } })._core;
  const out: ProvidedLink[] = [];
  for (const provider of core._linkProviderService.linkProviders) {
    provider.provideLinks(y, (links) => out.push(...(links ?? [])));
  }
  return out;
}

function mouse(type: string, init: MouseEventInit = {}): MouseEvent {
  return new MouseEvent(type, { clientX: 40, clientY: 20, cancelable: true, ...init });
}

describe("terminal links in a real xterm", () => {
  let host: HTMLDivElement;
  let term: Terminal;
  let activation: TerminalLinkActivation;
  let opened: string[];
  let controller: ReturnType<typeof createTerminalLinkController>;

  beforeEach(() => {
    window.matchMedia = (() => ({
      matches: false,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
    })) as unknown as typeof window.matchMedia;
    host = document.createElement("div");
    host.className = "nx-xterm-host";
    document.body.appendChild(host);
    activation = "modifier";
    opened = [];
    controller = createTerminalLinkController({
      tooltipHost: () => host,
      cursorHost: host,
      activation: () => activation,
      open: (uri) => opened.push(uri),
      mac: false,
    });
    term = new Terminal({ allowProposedApi: true, cols: 80, rows: 10, linkHandler: controller.linkHandler });
    term.open(host);
    term.loadAddon(new WebLinksAddon(controller.webLinkHandler, controller.webLinkOptions));
  });

  afterEach(() => {
    controller.dispose();
    term.dispose();
    host.remove();
  });

  const write = (data: string) => new Promise<void>((resolve) => term.write(data, resolve));

  it("opens OSC 8 links only with the modifier, using the target rather than the text", async () => {
    await write("\x1b]8;;https://example.com/target\x1b\\click here\x1b]8;;\x1b\\\r\n");
    const [link] = linksOnLine(term, 1);
    expect(link.text).toBe("https://example.com/target");
    link.activate(mouse("mouseup"), link.text);
    expect(opened).toEqual([]);
    link.activate(mouse("mouseup", { ctrlKey: true }), link.text);
    expect(opened).toEqual(["https://example.com/target"]);
  });

  it("opens detected URLs only with the modifier", async () => {
    await write("see https://example.com/docs for details\r\n");
    const [link] = linksOnLine(term, 1);
    expect(link.text).toBe("https://example.com/docs");
    link.activate(mouse("mouseup"), link.text);
    expect(opened).toEqual([]);
    link.activate(mouse("mouseup", { ctrlKey: true }), link.text);
    expect(opened).toEqual(["https://example.com/docs"]);
  });

  it("opens on a plain click when the preference says so", async () => {
    activation = "click";
    await write("\x1b]8;;file://web01/etc/hosts\x1b\\hosts\x1b]8;;\x1b\\ and https://example.com/\r\n");
    const links = linksOnLine(term, 1);
    expect(links.map((l) => l.text)).toEqual(["file://web01/etc/hosts", "https://example.com/"]);
    for (const link of links) link.activate(mouse("mouseup"), link.text);
    expect(opened).toEqual(["file://web01/etc/hosts", "https://example.com/"]);
  });

  it("hands non-HTTP OSC 8 targets to the policy instead of xterm's confirm/open default", async () => {
    const confirm = vi.spyOn(window, "confirm");
    const open = vi.spyOn(window, "open");
    await write("\x1b]8;;javascript:alert(1)\x1b\\click me\x1b]8;;\x1b\\\r\n");
    const [link] = linksOnLine(term, 1);
    link.activate(mouse("mouseup", { ctrlKey: true }), link.text);
    expect(opened).toEqual(["javascript:alert(1)"]);
    expect(planTerminalLink(opened[0]).kind).toBe("blocked");
    expect(confirm).not.toHaveBeenCalled();
    expect(open).not.toHaveBeenCalled();
  });

  it("shows the real target in a tooltip only while the modifier is held", async () => {
    await write("\x1b]8;;https://example.com/real\x1b\\innocent text\x1b]8;;\x1b\\\r\n");
    const [link] = linksOnLine(term, 1);
    link.hover?.(mouse("mousemove"), link.text);
    expect(controller.hovered()).toBe("https://example.com/real");
    expect(host.querySelector(".nx-term-link-tip")).toBeNull();
    expect(host.dataset.nxLinkArmed).toBe("false");

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Control", ctrlKey: true }));
    const tip = host.querySelector(".nx-term-link-tip");
    expect(tip?.textContent).toContain("https://example.com/real");
    expect(tip?.textContent).toContain("Ctrl+单击打开");
    expect(host.dataset.nxLinkArmed).toBe("true");

    window.dispatchEvent(new KeyboardEvent("keyup", { key: "Control", ctrlKey: false }));
    expect(host.querySelector(".nx-term-link-tip")).toBeNull();

    link.hover?.(mouse("mousemove", { ctrlKey: true }), link.text);
    expect(host.querySelector(".nx-term-link-tip")).not.toBeNull();
    link.leave?.(mouse("mousemove"), link.text);
    expect(host.querySelector(".nx-term-link-tip")).toBeNull();
    expect(controller.hovered()).toBeNull();
  });

  it("shows the tooltip on plain hover in click mode, with sanitized text", async () => {
    activation = "click";
    await write("\x1b]8;;https://bücher.example/\x1b\\books\x1b]8;;\x1b\\\r\n");
    const [link] = linksOnLine(term, 1);
    link.hover?.(mouse("mousemove"), link.text);
    const tip = host.querySelector(".nx-term-link-tip");
    expect(tip?.textContent).toContain("https://xn--bcher-kva.example/");
    expect(tip?.textContent).toContain("单击打开");
  });

  it("uses Cmd on macOS", () => {
    const mac = createTerminalLinkController({
      tooltipHost: () => host,
      cursorHost: host,
      activation: () => "modifier",
      open: (uri) => opened.push(uri),
      mac: true,
    });
    mac.webLinkHandler(mouse("mouseup", { ctrlKey: true }), "https://a.example/");
    mac.webLinkHandler(mouse("mouseup", { metaKey: true }), "https://b.example/");
    expect(opened).toEqual(["https://b.example/"]);
    mac.webLinkOptions.hover(mouse("mousemove", { metaKey: true }), "https://b.example/");
    expect(host.querySelector(".nx-term-link-tip")?.textContent).toContain("⌘+单击打开");
    mac.dispose();
  });
});

describe("touch long-press on links", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  function touch(target: HTMLElement, type: string, points: { x: number; y: number }[]) {
    const event = new Event(type, { bubbles: true });
    const touches = points.map((p) => ({ clientX: p.x, clientY: p.y }));
    Object.defineProperty(event, "touches", { value: touches });
    target.dispatchEvent(event);
  }

  it("offers the link under a held finger", () => {
    const host = document.createElement("div");
    const probe = vi.fn((x: number) => (x < 100 ? "https://example.com/" : null));
    const onLinkMenu = vi.fn();
    const detach = attachLinkLongPress(host, { probe, onLinkMenu });
    touch(host, "touchstart", [{ x: 20, y: 30 }]);
    vi.advanceTimersByTime(LINK_LONG_PRESS_MS - 1);
    expect(onLinkMenu).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(probe).toHaveBeenCalledWith(20, 30);
    expect(onLinkMenu).toHaveBeenCalledWith({ uri: "https://example.com/", x: 20, y: 30 });

    // No link under the finger: no menu.
    touch(host, "touchstart", [{ x: 200, y: 30 }]);
    vi.advanceTimersByTime(LINK_LONG_PRESS_MS);
    expect(onLinkMenu).toHaveBeenCalledTimes(1);
    detach();
  });

  it("ignores taps, drags and multi-touch", () => {
    const host = document.createElement("div");
    const probe = vi.fn(() => "https://example.com/");
    const onLinkMenu = vi.fn();
    const detach = attachLinkLongPress(host, { probe, onLinkMenu });
    touch(host, "touchstart", [{ x: 20, y: 30 }]);
    touch(host, "touchend", []);
    touch(host, "touchstart", [{ x: 20, y: 30 }]);
    touch(host, "touchmove", [{ x: 60, y: 30 }]);
    touch(host, "touchstart", [{ x: 20, y: 30 }, { x: 80, y: 30 }]);
    vi.advanceTimersByTime(LINK_LONG_PRESS_MS * 2);
    expect(probe).not.toHaveBeenCalled();
    detach();
    touch(host, "touchstart", [{ x: 20, y: 30 }]);
    vi.advanceTimersByTime(LINK_LONG_PRESS_MS);
    expect(onLinkMenu).not.toHaveBeenCalled();
  });
});

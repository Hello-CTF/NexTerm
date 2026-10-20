/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

vi.mock("../ipc/commands", () => ({
  layoutApi: { get: vi.fn(), put: vi.fn() },
  terminalApi: { listLive: vi.fn(), closeTab: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../ipc/events", () => ({ listenEvent: vi.fn() }));

import { useUi, type AppTab } from "../app/store";
import {
  applyRemoteTabTitleToView,
  getTerminalLinesBelow,
  getTerminalTabTitle,
  resetTerminalView,
  setTerminalLinesBelow,
  setTerminalTabTitle,
  useTerminalLinesBelow,
  useTerminalTabTitle,
} from "../app/terminalViewStore";
import { mount, type MountedView } from "./features/reactTestUtils";

describe("terminal view store selectors", () => {
  let renders: string[];
  let mounted: MountedView | undefined;

  function Probe({ tabId }: { tabId: string }) {
    const linesBelow = useTerminalLinesBelow(tabId);
    const title = useTerminalTabTitle(tabId);
    renders.push(`${linesBelow}|${title ?? ""}`);
    return null;
  }

  beforeEach(() => {
    mounted?.unmount();
    act(() => resetTerminalView());
    renders = [];
    mounted = mount(createElement(Probe, { tabId: "t1" }));
  });

  it("renders the initial zero state without lines below or title", () => {
    expect(renders).toEqual(["0|"]);
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("re-renders only when the subscribed tab values change", () => {
    act(() => setTerminalLinesBelow("t1", 5));
    expect(renders).toEqual(["0|", "5|"]);

    // Another tab's scrolling must not re-render this subscriber.
    act(() => {
      setTerminalLinesBelow("t2", 9);
      setTerminalTabTitle("t2", "htop");
    });
    expect(renders).toEqual(["0|", "5|"]);

    act(() => setTerminalTabTitle("t1", "vim"));
    expect(renders).toEqual(["0|", "5|", "5|vim"]);
    expect(getTerminalTabTitle("t1")).toBe("vim");
  });

  it("treats unchanged values as no-ops so output bursts do not re-render", () => {
    act(() => setTerminalLinesBelow("t1", 5));
    const before = renders.length;
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalLinesBelow("t1", 5);
    });
    expect(renders).toHaveLength(before);
  });

  it("clears every tracked value on reset", () => {
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalTabTitle("t1", "vim");
    });
    act(() => resetTerminalView());
    // React batches both sets inside one act() into a single re-render.
    expect(renders).toEqual(["0|", "5|vim", "0|"]);
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("drops only the reset tab when a tab id is given", () => {
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalLinesBelow("t2", 9);
    });
    act(() => resetTerminalView("t2"));
    expect(getTerminalLinesBelow("t1")).toBe(5);
    expect(getTerminalLinesBelow("t2")).toBe(0);
  });
});

function terminalTab(id = "t1", extra: Partial<AppTab> = {}): AppTab {
  return {
    id,
    kind: "terminal",
    title: "Terminal 1",
    sessionId: "s",
    tabId: `kernel-${id}`,
    closable: true,
    ...extra,
  };
}

function seedWorkspaces(tabs: AppTab[]) {
  useUi.setState({
    workspaces: [
      {
        id: "ws",
        kind: "session",
        title: "ws",
        panes: [{ id: "pane", tabs, activeTabId: tabs[0]?.id ?? null }],
        activePaneId: "pane",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws",
    sessions: [],
    toasts: [],
  });
}

function workspaceTitleOf(id = "t1"): string | undefined {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return t.title;
    }
  }
  return undefined;
}

describe("applyRemoteTabTitleToView (OSC title chain)", () => {
  let renders: string[];
  let mounted: MountedView | undefined;

  function Probe({ tabId }: { tabId: string }) {
    const title = useTerminalTabTitle(tabId);
    renders.push(title ?? "");
    return null;
  }

  beforeEach(() => {
    localStorage.clear();
    mounted?.unmount();
    act(() => resetTerminalView());
    seedWorkspaces([terminalTab()]);
    renders = [];
    mounted = mount(createElement(Probe, { tabId: "t1" }));
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("lands an accepted remote title in the workspace tab and the view store", () => {
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "user@host: ~/proj")).toBe(true);
    });
    expect(workspaceTitleOf()).toBe("user@host: ~/proj");
    expect(getTerminalTabTitle("t1")).toBe("user@host: ~/proj");
    expect(renders).toEqual(["", "user@host: ~/proj"]);
  });

  it("keeps a user-renamed tab authoritative in both stores", () => {
    seedWorkspaces([terminalTab("t1", { title: "my terminal", renamedByUser: true })]);
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "remote title")).toBe(false);
    });
    expect(workspaceTitleOf()).toBe("my terminal");
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(renders).toEqual([""]);
  });

  it("writes nothing for unknown tabs", () => {
    act(() => {
      expect(applyRemoteTabTitleToView("missing", "title")).toBe(false);
    });
    expect(getTerminalTabTitle("missing")).toBeUndefined();
  });

  it("dedups repeated identical OSC titles so a burst re-renders nothing", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
    });
    expect(renders).toEqual(["", "vim"]);
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
      applyRemoteTabTitleToView("t1", "vim");
    });
    expect(renders).toEqual(["", "vim"]);
  });

  it("stores the sanitized title in both stores", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "\x07evil\x00 title\nnext\tline");
    });
    expect(workspaceTitleOf()).toBe("evil title next line");
    expect(getTerminalTabTitle("t1")).toBe("evil title next line");
  });

  it("a rename after a remote title clears the view entry and locks out later OSC titles", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
    });
    act(() => {
      useUi.getState().updateTab("t1", { title: "locked", renamedByUser: true });
      resetTerminalView("t1");
    });
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(renders).toEqual(["", "vim", ""]);
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "other")).toBe(false);
    });
    expect(workspaceTitleOf()).toBe("locked");
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("account-switch cleanup leaves no view state and rejects a late OSC title for a gone tab", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
      setTerminalLinesBelow("kernel-t1", 42);
    });
    act(() => {
      // The account switch clears the workspaces; the per-tab unmount cleanups
      // aggregate to a full view-store reset.
      useUi.getState().resetAccountWorkspaces();
      resetTerminalView();
    });
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(getTerminalLinesBelow("kernel-t1")).toBe(0);
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "late title")).toBe(false);
    });
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });
});

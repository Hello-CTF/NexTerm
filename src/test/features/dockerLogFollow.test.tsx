/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  flush,
  mount,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ps: vi.fn(),
  images: vi.fn(),
  logsAttach: vi.fn(),
  closeTab: vi.fn(),
  bytesCb: null as null | ((bytes: Uint8Array) => void),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      ps: mocks.ps,
      images: mocks.images,
      inspect: vi.fn(),
      stats: vi.fn(),
      containerListDir: vi.fn(),
      action: vi.fn(),
      imageRemove: vi.fn(),
      imagePull: vi.fn(),
      logsAttach: mocks.logsAttach,
      execAttach: vi.fn(),
    },
    terminalApi: { closeTab: mocks.closeTab },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createBinaryChannel: vi.fn((cb: (bytes: Uint8Array) => void) => {
      mocks.bytesCb = cb;
      return { id: "ch-1" };
    }),
    disposeChannel: vi.fn(),
    onChannelReopen: () => () => undefined,
  };
});

import { createLogLineDecoder, DockerPanel } from "../../features/docker/DockerPanel";
import type { ContainerSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";

const containerA: ContainerSummary = {
  id: "aaaa11112222",
  name: "web",
  image: "nginx:1.25",
  state: "running",
  status: "Up 2 hours",
  ports: "80/tcp",
  composeProject: null,
};

function stubScroller(el: HTMLDivElement) {
  const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };
  for (const key of ["scrollTop", "scrollHeight", "clientHeight"] as const) {
    Object.defineProperty(el, key, {
      configurable: true,
      get: () => metrics[key],
      set: (value: number) => {
        metrics[key] = value;
      },
    });
  }
  return {
    metrics,
    setMetrics(patch: Partial<typeof metrics>) {
      Object.assign(metrics, patch);
    },
    dispatchScroll() {
      act(() => {
        el.dispatchEvent(new Event("scroll"));
      });
    },
  };
}

function emitLog(text: string): void {
  act(() => {
    mocks.bytesCb?.(new TextEncoder().encode(text));
  });
}

let mounted: MountedView | undefined;

function mountPanel(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(DockerPanel, { sessionId: "s1", visible: true }),
    ),
  );
}

function mountSwitchablePanel(initialSessionId: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const renderPanel = (sessionId: string) => createElement(
    QueryClientProvider,
    { client },
    createElement(DockerPanel, { sessionId, visible: true }),
  );
  const view = mount(renderPanel(initialSessionId));
  return {
    view,
    switchTo(sessionId: string) {
      act(() => view.root.render(renderPanel(sessionId)));
    },
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: vi.fn(), resyncSessions: vi.fn().mockResolvedValue(undefined), sessions: [] });
  mocks.bytesCb = null;
  mocks.ps.mockResolvedValue([containerA]);
  mocks.images.mockResolvedValue([]);
  mocks.logsAttach.mockResolvedValue("tab-1");
  mocks.closeTab.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("docker log stream follow", () => {
  async function openLogView(m: MountedView) {
    await waitFor(() =>
      expect(m.container.querySelector('button[title="查看日志"]')).not.toBeNull(),
    );
    click(m.container.querySelector('button[title="查看日志"]')!);
    await flush();
    await waitFor(() => expect(m.container.querySelector('[role="log"]')).not.toBeNull());
    return m.container.querySelector<HTMLDivElement>('[role="log"]')!;
  }

  it("exposes the log container with role=log and a label", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    expect(log.getAttribute("role")).toBe("log");
    expect(log.getAttribute("aria-label")).toBe("容器日志");
  });

  it("decodes Chinese across byte chunks and caches the unterminated tail", async () => {
    const m = (mounted = mountPanel());
    await openLogView(m);
    const encoded = new TextEncoder().encode("中文第一行\n");

    act(() => {
      mocks.bytesCb?.(encoded.slice(0, 2));
    });
    expect(m.container.textContent).not.toContain("中文");
    expect(m.container.textContent).not.toContain("�");

    act(() => {
      mocks.bytesCb?.(encoded.slice(2));
    });
    await waitFor(() => expect(m.container.textContent).toContain("中文第一行"));
    expect(m.container.textContent).not.toContain("�");

    emitLog("缓存");
    expect(m.container.textContent).not.toContain("缓存");
    emitLog("尾行\n");
    expect(m.container.textContent).toContain("缓存尾行");
  });

  it("flushes the cached tail when the decoder closes", () => {
    const decoder = createLogLineDecoder();
    const encoded = new TextEncoder().encode("中文尾行");
    expect(decoder.write(encoded.slice(0, 4))).toEqual([]);
    expect(decoder.write(encoded.slice(4))).toEqual([]);
    expect(decoder.close()).toEqual(["中文尾行"]);
  });

  it("follows new output while at the bottom", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    const sc = stubScroller(log);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    sc.dispatchScroll();
    emitLog("first line\n");
    expect(sc.metrics.scrollTop).toBe(1000);
    expect(mounted.container.textContent).not.toContain("↓ 新输出");
  });

  it("stops forcing scroll after the user scrolls up and offers a jump back", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    const sc = stubScroller(log);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    sc.dispatchScroll();
    emitLog("first line\n");
    expect(sc.metrics.scrollTop).toBe(1000);

    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    sc.setMetrics({ scrollHeight: 1600 });
    emitLog("second line\n");
    expect(sc.metrics.scrollTop).toBe(100);
    expect(mounted.container.textContent).toContain("↓ 新输出");

    clickButton(mounted.container, "↓ 新输出");
    expect(sc.metrics.scrollTop).toBe(1600);
    expect(mounted.container.textContent).not.toContain("↓ 新输出");

    sc.setMetrics({ scrollHeight: 2000 });
    emitLog("third line\n");
    expect(sc.metrics.scrollTop).toBe(2000);
  });

  it("keeps the follow state across log lines without spurious new-output badges", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    const sc = stubScroller(log);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    sc.dispatchScroll();
    emitLog("first line\n");
    sc.setMetrics({ scrollHeight: 1200 });
    emitLog("second line\n");
    expect(sc.metrics.scrollTop).toBe(1200);
    expect(mounted.container.textContent).not.toContain("↓ 新输出");
  });

  it("reopens logs on the same host after reconnect and exposes the discontinuity", async () => {
    useUi.setState({ sessions: [{ id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
    mocks.logsAttach.mockResolvedValueOnce("tab-1").mockResolvedValueOnce("tab-2");
    mounted = mountPanel();
    await openLogView(mounted);
    emitLog("before reconnect\n");
    expect(mounted.container.textContent).toContain("before reconnect");

    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "reconnecting" as const })) }));
    });
    await waitFor(() => expect(mounted?.container.textContent).toContain("连接恢复后将重新读取最近 500 行"));
    expect(mounted.container.querySelector<HTMLButtonElement>("button:disabled")?.textContent).toBeTruthy();
    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
    });
    await waitFor(() => expect(mocks.logsAttach).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mounted?.container.textContent).toContain("断线期间超过 500 行的内容可能缺失"));
    expect(mocks.logsAttach.mock.calls[1]?.slice(0, 3)).toEqual(["s1", "aaaa11112222", 500]);
    expect(mounted.container.textContent).not.toContain("before reconnect");
  });

  it("does not reopen logs when the same session ID points to another asset", async () => {
    useUi.setState({ sessions: [{ id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
    mounted = mountPanel();
    await openLogView(mounted);
    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, assetId: "host-b", status: "reconnecting" as const })) }));
    });
    await waitFor(() => expect(mounted?.container.querySelector('[role="log"]')).toBeNull());
    await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("tab-1", "kill", "s1"));
    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
    });
    await flush();
    expect(mocks.logsAttach).toHaveBeenCalledTimes(1);
  });

  it("closes an active old stream when the panel switches sessions", async () => {
    useUi.setState({ sessions: [
      { id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
      { id: "s2", assetId: "host-b", name: "host B", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
    ] });
    const panel = mountSwitchablePanel("s1");
    mounted = panel.view;
    await openLogView(mounted);
    emitLog("host A secret line\n");
    expect(mounted.container.textContent).toContain("host A secret line");

    panel.switchTo("s2");
    expect(mounted.container.querySelector('[role="log"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("host A secret line");
    await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("tab-1", "kill", "s1"));
  });

  it("discards a delayed old attach after switching sessions and opens only the new host", async () => {
    useUi.setState({ sessions: [
      { id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
      { id: "s2", assetId: "host-b", name: "host B", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
    ] });
    let completeOldAttach: (id: string) => void = () => {};
    mocks.logsAttach.mockImplementationOnce(() => new Promise<string>((resolve) => { completeOldAttach = resolve; }))
      .mockResolvedValueOnce("new-tab");
    const panel = mountSwitchablePanel("s1");
    mounted = panel.view;
    await waitFor(() => expect(mounted?.container.querySelector('button[title="查看日志"]')).not.toBeNull());
    click(mounted.container.querySelector('button[title="查看日志"]')!);
    await waitFor(() => expect(mocks.logsAttach).toHaveBeenCalledTimes(1));

    panel.switchTo("s2");
    expect(mounted.container.querySelector('[role="log"]')).toBeNull();
    await waitFor(() => expect(mounted?.container.querySelector('button[title="查看日志"]')).not.toBeNull());
    click(mounted.container.querySelector('button[title="查看日志"]')!);
    await waitFor(() => expect(mocks.logsAttach).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mounted?.container.querySelector('[role="log"]')).not.toBeNull());
    expect(mocks.logsAttach.mock.calls[1]?.[0]).toBe("s2");
    emitLog("host B line\n");
    expect(mounted.container.textContent).toContain("host B line");

    await act(async () => { completeOldAttach("old-tab"); });
    await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("old-tab", "kill", "s1"));
    expect(mounted.container.textContent).toContain("host B line");
    expect(mounted.container.textContent).not.toContain("host A secret line");
  });

  it("discards a delayed log attach if the target host changes before it completes", async () => {
    useUi.setState({ sessions: [{ id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
    let completeAttach: (id: string) => void = () => {};
    mocks.logsAttach.mockImplementationOnce(() => new Promise<string>((resolve) => { completeAttach = resolve; }));
    mounted = mountPanel();
    await waitFor(() => expect(mounted?.container.querySelector('button[title="查看日志"]')).not.toBeNull());
    click(mounted.container.querySelector('button[title="查看日志"]')!);
    await waitFor(() => expect(mocks.logsAttach).toHaveBeenCalledTimes(1));
    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, assetId: "host-b" })) }));
    });
    await act(async () => { completeAttach("late-tab"); });
    await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("late-tab", "kill", "s1"));
    expect(mounted.container.querySelector('[role="log"]')).toBeNull();
  });

  it("discards an old stream even when reconnect finishes before attach returns", async () => {
    useUi.setState({ sessions: [{ id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
    let completeAttach: (id: string) => void = () => {};
    mocks.logsAttach.mockImplementationOnce(() => new Promise<string>((resolve) => { completeAttach = resolve; }));
    mounted = mountPanel();
    await waitFor(() => expect(mounted?.container.querySelector('button[title="查看日志"]')).not.toBeNull());
    click(mounted.container.querySelector('button[title="查看日志"]')!);
    await waitFor(() => expect(mocks.logsAttach).toHaveBeenCalledTimes(1));
    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "reconnecting" as const })) }));
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
    });
    await act(async () => { completeAttach("stale-tab"); });
    await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("stale-tab", "kill", "s1"));
    expect(mounted.container.querySelector('[role="log"]')).toBeNull();
    expect(useUi.getState().pushToast).toHaveBeenCalledWith("info", "打开日志时主机连接发生变化，请重新打开日志");
  });
});

/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";
import type { ContainerSummary, SessionInfo } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  ps: vi.fn(),
  images: vi.fn(),
  composeStacks: vi.fn(),
  forwardAndOpen: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: { ...actual.dockerApi, ps: mocks.ps, images: mocks.images, composeStacks: mocks.composeStacks },
  };
});
vi.mock("../../features/preview/forwardAndOpen", () => ({ forwardAndOpen: mocks.forwardAndOpen }));

import { DockerPanel } from "../../features/docker/DockerPanel";
import { dialHostFor, parsePublishedPorts } from "../../features/docker/containerPorts";
import { useUi } from "../../app/store";

describe("parsePublishedPorts", () => {
  it("parses moby and docker CLI formats and ignores unpublished ports", () => {
    expect(parsePublishedPorts("0.0.0.0:8080->80/tcp, [::]:8080->80/tcp, 443/tcp")).toEqual([
      { hostIp: "0.0.0.0", hostPort: 8080, containerPort: 80, protocol: "tcp" },
    ]);
    expect(parsePublishedPorts("0.0.0.0:8080->80/tcp, :::8080->80/tcp")).toHaveLength(1);
    expect(parsePublishedPorts("8080->80/tcp")).toEqual([{ hostIp: "", hostPort: 8080, containerPort: 80, protocol: "tcp" }]);
    expect(parsePublishedPorts("80/tcp")).toEqual([]);
    expect(parsePublishedPorts("")).toEqual([]);
  });

  it("keeps tcp only", () => {
    expect(parsePublishedPorts("0.0.0.0:53->53/udp, 0.0.0.0:80->80/tcp").map((p) => p.hostPort)).toEqual([80]);
  });

  it("prefers the IPv4 entry regardless of order", () => {
    expect(parsePublishedPorts("[::]:8080->80/tcp, 0.0.0.0:8080->80/tcp")).toEqual([
      { hostIp: "0.0.0.0", hostPort: 8080, containerPort: 80, protocol: "tcp" },
    ]);
    expect(parsePublishedPorts(":::8080->80/tcp")[0].hostIp).toBe("::");
  });

  it("expands ranges up to 16 ports", () => {
    expect(parsePublishedPorts("0.0.0.0:8000-8001->9000-9001/tcp").map((p) => [p.hostPort, p.containerPort])).toEqual([[8000, 9000], [8001, 9001]]);
    expect(parsePublishedPorts("0.0.0.0:8000-8100->8000-8100/tcp")).toHaveLength(16);
  });
});

describe("dialHostFor", () => {
  it("maps wildcard binds to loopback and keeps specific addresses", () => {
    expect(dialHostFor("0.0.0.0")).toBe("127.0.0.1");
    expect(dialHostFor("::")).toBe("127.0.0.1");
    expect(dialHostFor("")).toBe("127.0.0.1");
    expect(dialHostFor("127.0.0.1")).toBe("127.0.0.1");
    expect(dialHostFor("192.168.1.5")).toBe("192.168.1.5");
    expect(dialHostFor("[::1]")).toBe("::1");
  });
});

const container = (id: string, state: string, ports: string): ContainerSummary => ({
  id, name: id, image: "example", state, status: state, ports, composeProject: null,
});

const session = (kind: string, status: SessionInfo["status"]): SessionInfo => ({
  id: "s1", assetId: null, name: "host", kind, status, tabs: [], createdAt: 0,
});

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.images.mockResolvedValue([]);
  mocks.composeStacks.mockResolvedValue([]);
  mocks.forwardAndOpen.mockResolvedValue(undefined);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function mountPanel(info: SessionInfo, containers: ContainerSummary[]) {
  useUi.setState({ sessions: [info] });
  mocks.ps.mockResolvedValue(containers);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(DockerPanel, { sessionId: "s1" })));
  await flushUntil(() => (mounted?.container.textContent ?? "").includes("web"));
}

const portButtons = () => mounted!.container.querySelectorAll<HTMLButtonElement>('button[aria-label^="转发并打开端口"]');

describe("container port forwarding buttons", () => {
  it("forwards a published port of a running container on a connected ssh session", async () => {
    await mountPanel(session("ssh", "connected"), [container("web", "running", "0.0.0.0:8080->80/tcp, [::]:8080->80/tcp")]);
    expect(portButtons()).toHaveLength(1);
    expect(portButtons()[0].getAttribute("aria-label")).toBe("转发并打开端口 8080");
    portButtons()[0].click();
    expect(mocks.forwardAndOpen).toHaveBeenCalledWith({ sessionId: "s1", host: "127.0.0.1", port: 8080 });
  });

  it("dials a specific bind address as is", async () => {
    await mountPanel(session("ssh", "connected"), [container("web", "running", "192.168.1.5:9090->80/tcp")]);
    portButtons()[0].click();
    expect(mocks.forwardAndOpen).toHaveBeenCalledWith({ sessionId: "s1", host: "192.168.1.5", port: 9090 });
  });

  it("is hidden for non-ssh sessions, disconnected sessions and stopped containers", async () => {
    await mountPanel(session("local", "connected"), [container("web", "running", "0.0.0.0:8080->80/tcp")]);
    expect(portButtons()).toHaveLength(0);
    mounted!.unmount();
    await mountPanel(session("ssh", "disconnected"), [container("web", "running", "0.0.0.0:8080->80/tcp")]);
    expect(portButtons()).toHaveLength(0);
    mounted!.unmount();
    await mountPanel(session("ssh", "connected"), [container("web", "exited", "0.0.0.0:8080->80/tcp")]);
    expect(portButtons()).toHaveLength(0);
  });
});

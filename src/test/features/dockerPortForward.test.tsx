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
vi.mock("../../features/preview/forwardAndOpen", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../features/preview/forwardAndOpen")>()),
  forwardAndOpen: mocks.forwardAndOpen,
}));

import { DockerPanel } from "../../features/docker/DockerPanel";
import { parsePublishedPorts } from "../../features/docker/containerPorts";
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

describe("parsePublishedPorts host validation", () => {
  it("accepts empty, IPv4 and IPv6 literals", () => {
    expect(parsePublishedPorts("[2001:db8::1]:80->80/tcp")[0].hostIp).toBe("2001:db8::1");
    expect(parsePublishedPorts("[::ffff:192.168.1.5]:80->80/tcp")[0].hostIp).toBe("::ffff:192.168.1.5");
    expect(parsePublishedPorts("[1:2:3:4:5:6:7:8]:80->80/tcp")).toHaveLength(1);
    expect(parsePublishedPorts("127.0.0.1:80->80/tcp")).toHaveLength(1);
  });

  it("skips entries whose host is not an IP literal", () => {
    for (const bad of [
      "evil.example:80->80/tcp",
      "1.2.3.999:80->80/tcp",
      "1.2.3:80->80/tcp",
      "[fe80::1%eth0]:80->80/tcp",
      "[1:2:3:4:5:6:7:8:9]:80->80/tcp",
      "[1::2::3]:80->80/tcp",
      "[g::1]:80->80/tcp",
      "1.2.3.4 :80->80/tcp",
      "[::1:80->80/tcp",
    ]) {
      expect(parsePublishedPorts(bad), bad).toEqual([]);
    }
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
  const clickedHost = async (ports: string) => {
    await mountPanel(session("ssh", "connected"), [container("web", "running", ports)]);
    expect(portButtons()).toHaveLength(1);
    portButtons()[0].click();
    return mocks.forwardAndOpen.mock.calls.at(-1)?.[0];
  };

  it("dials loopback for a wildcard IPv4 bind", async () => {
    expect(await clickedHost("0.0.0.0:8080->80/tcp")).toEqual({ sessionId: "s1", host: "127.0.0.1", port: 8080 });
  });

  it("dials ::1 for an IPv6-only wildcard bind", async () => {
    expect(await clickedHost("[::]:8080->80/tcp")).toMatchObject({ host: "::1", port: 8080 });
  });

  it("prefers loopback IPv4 when the same port is also bound on IPv4", async () => {
    expect(await clickedHost(":::8080->80/tcp, 0.0.0.0:8080->80/tcp")).toMatchObject({ host: "127.0.0.1" });
  });

  it("dials a specific bind address as is", async () => {
    expect(await clickedHost("192.168.1.5:9090->80/tcp")).toEqual({ sessionId: "s1", host: "192.168.1.5", port: 9090 });
  });

  it("shows no button for invalid host addresses", async () => {
    await mountPanel(session("ssh", "connected"), [container("web", "running", "evil.example:8080->80/tcp, 1.2.3.999:80->80/tcp")]);
    expect(portButtons()).toHaveLength(0);
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

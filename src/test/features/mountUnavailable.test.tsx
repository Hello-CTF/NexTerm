/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    mountApi: { list: mocks.list, create: vi.fn(), remove: vi.fn() },
    systemApi: { platform: vi.fn().mockResolvedValue("linux") },
    sessionApi: {},
  };
});

import { setMountUnavailableReason } from "../../app/capabilities";
import { MountPanel } from "../../features/files/MountPanel";

const reason = "多用户服务端不支持挂载：它会在服务器上用服务进程的 SSH 配置和凭据执行 sshfs";
let mounted: MountedView | undefined;
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setMountUnavailableReason(null);
});

describe("MountPanel capability reason", () => {
  it("shows the server-provided reason and never queries mounts", () => {
    setMountUnavailableReason(reason);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(MountPanel, { sessionId: "s1" })),
    );
    expect(mounted.container.textContent).toContain(reason);
    expect(mocks.list).not.toHaveBeenCalled();
  });
});

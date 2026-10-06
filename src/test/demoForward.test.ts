import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

interface DemoForward {
  id: string;
  sessionId: string;
  listenHost?: string;
  listenPort: number;
  targetHost: string | null;
  targetPort: number | null;
  kind: string;
  createdAt: number;
}

async function listForwards(): Promise<DemoForward[]> {
  return (await mockInvoke("forward_list")) as DemoForward[];
}

describe("demo 远程转发 mock", () => {
  it("回环绑定默认 127.0.0.1，无需确认即可创建并进入列表 DTO", async () => {
    const before = await listForwards();
    const created = (await mockInvoke("forward_create_remote", {
      sessionId: "s-web01",
      bindPort: 18080,
      targetHost: "127.0.0.1",
      targetPort: 3306,
    })) as DemoForward;
    expect(created.kind).toBe("remote");
    expect(created.listenHost).toBe("127.0.0.1");
    expect(created.listenPort).toBe(18080);
    expect(created.targetHost).toBe("127.0.0.1");
    expect(created.targetPort).toBe(3306);

    const listed = await listForwards();
    expect(listed).toHaveLength(before.length + 1);
    const entry = listed.find((f) => f.id === created.id);
    expect(entry?.listenHost).toBe("127.0.0.1");
    expect(entry?.kind).toBe("remote");
  });

  it("显式回环地址（localhost / 127.x）不需要风险确认", async () => {
    for (const bindHost of ["localhost", "127.0.0.2"]) {
      const created = (await mockInvoke("forward_create_remote", {
        sessionId: "s-web01",
        bindHost,
        bindPort: 18081,
        targetHost: "127.0.0.1",
        targetPort: 3306,
      })) as DemoForward;
      expect(created.listenHost).toBe(bindHost);
    }
  });

  it("非回环绑定未确认时抛 needs_confirm 且不写入列表", async () => {
    const before = await listForwards();
    await expect(
      mockInvoke("forward_create_remote", {
        sessionId: "s-web01",
        bindHost: "0.0.0.0",
        bindPort: 18082,
        targetHost: "127.0.0.1",
        targetPort: 3306,
      }),
    ).rejects.toMatchObject({
      code: "needs_confirm",
      detail: {
        risk: "unauthenticated_exposed_remote_forward",
        listenHost: "0.0.0.0",
        authentication: "none",
      },
    });
    expect(await listForwards()).toHaveLength(before.length);
  });

  it("acknowledgeRisk 后创建成功，listenHost 保留远端绑定地址", async () => {
    const created = (await mockInvoke("forward_create_remote", {
      sessionId: "s-web01",
      bindHost: "0.0.0.0",
      bindPort: 18083,
      targetHost: "127.0.0.1",
      targetPort: 3306,
      acknowledgeRisk: true,
    })) as DemoForward;
    expect(created.listenHost).toBe("0.0.0.0");
    const listed = await listForwards();
    expect(listed.find((f) => f.id === created.id)?.listenHost).toBe("0.0.0.0");
    await mockInvoke("forward_remove", { id: created.id });
    expect((await listForwards()).find((f) => f.id === created.id)).toBeUndefined();
  });
});

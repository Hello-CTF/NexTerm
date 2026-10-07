/** @vitest-environment jsdom */
// FLEET156 supervisor v2 二进制合同测试: 帧编解码 (拆包/粘包/超长)、output 序列
// 间隙处理、hello 版本协商、create/attach/resume/input/resize/kill 与错误路径。
// 字节布局逐条对齐 internal/supervisor/protocol.go 与 server.go, 桥接形态对齐
// internal/fleet/server/ws.go (二进制 WS 消息即字节流)。
import { beforeEach, describe, expect, it } from "vitest";
import {
  DeviceBridge,
  DeviceTerminalError,
  SupervisorFrameKind,
  SupervisorFrameParser,
  encodeSupervisorFrame,
  encodeSupervisorJSONFrame,
  encodeSupervisorOutputPayload,
  killDeviceTerminal,
  openDeviceTerminal,
  parseSupervisorOutput,
  resumeDeviceTerminal,
  unixNanoFromRFC3339,
  SUPERVISOR_MAX_FRAME_PAYLOAD,
  SUPERVISOR_PROTOCOL_VERSION,
  type SupervisorFrame,
} from "../../ipc/deviceTerminalApi";

const INFO = {
  id: "s-1",
  created_at: "2026-10-07T05:34:56.123456789+08:00",
  incarnation: "inc-1",
  cols: 80,
  rows: 24,
  dead: false,
};

const CREATED_AT_NS = BigInt(Date.UTC(2026, 9, 6, 21, 34, 56) / 1000) * 1_000_000_000n + 123456789n;

// behavior 让每个用例按需注入设备端异常; 默认走 server.go 的标准应答。
const behavior: {
  helloError?: string;
  attachError?: string;
  expectDigest?: string;
  helloErrorFrom?: number;
  helloHangFrom?: number;
  createDefer?: boolean;
  killSessionError?: string;
} = {};

// lastCreateId 记录 fake 服务端最近一次 create 的会话 id (create 帧客户端
// 生成稳定 id+attempt, Created 按合同回显同一 id)。
let lastCreateId = "s-1";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static reset(): void {
    FakeWebSocket.instances = [];
  }

  readonly url: string;
  readonly sent: Uint8Array[] = [];
  binaryType = "";
  readyState = 1;
  private createPending = false;
  private parser = new SupervisorFrameParser();
  private listeners = new Map<string, ((event: unknown) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  addEventListener(type: string, listener: (event: unknown) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  send(data: ArrayBuffer | Uint8Array): void {
    const bytes = data instanceof Uint8Array ? data.slice() : new Uint8Array(data.slice(0));
    this.sent.push(bytes);
    for (const frame of this.parser.push(bytes)) this.respond(frame);
  }

  close(code = 1000, reason = ""): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.emit("close", { code, reason, wasClean: code === 1000 });
  }

  // serverClose 模拟对端拆线 (pipeBridge 的 1000 或异常断开), 与本地 close 区分。
  serverClose(code: number, reason: string): void {
    this.close(code, reason);
  }

  serverSend(frame: Uint8Array): void {
    this.emit("message", { data: frame.buffer.slice(frame.byteOffset, frame.byteOffset + frame.byteLength) });
  }

  serverJSON(kind: number, message: unknown): void {
    this.serverSend(encodeSupervisorJSONFrame(kind, message));
  }

  serverOutput(seq: bigint, text: string): void {
    this.serverSend(
      encodeSupervisorFrame(SupervisorFrameKind.Output, encodeSupervisorOutputPayload(seq, new TextEncoder().encode(text))),
    );
  }

  sentFrames(): SupervisorFrame[] {
    const parser = new SupervisorFrameParser();
    const frames: SupervisorFrame[] = [];
    for (const bytes of this.sent) frames.push(...parser.push(bytes));
    return frames;
  }

  sentPayloadText(kind: number): string {
    const frame = [...this.sentFrames()].reverse().find((f) => f.kind === kind);
    if (!frame) throw new Error(`frame ${kind} not sent`);
    return new TextDecoder().decode(frame.payload);
  }

  sentJSON(kind: number): unknown {
    return JSON.parse(this.sentPayloadText(kind));
  }

  private respond(frame: SupervisorFrame): void {
    switch (frame.kind) {
      case SupervisorFrameKind.Hello: {
        const hello = JSON.parse(new TextDecoder().decode(frame.payload)) as {
          version: number;
          state_digest?: string;
        };
        const index = FakeWebSocket.instances.indexOf(this) + 1;
        if (behavior.helloHangFrom !== undefined && index >= behavior.helloHangFrom) return;
        const helloError =
          behavior.helloError ??
          (behavior.helloErrorFrom !== undefined && index >= behavior.helloErrorFrom ? "internal" : undefined);
        if (helloError) {
          this.serverJSON(SupervisorFrameKind.Error, { code: helloError, message: helloError });
          return;
        }
        if (hello.version !== SUPERVISOR_PROTOCOL_VERSION) {
          this.serverJSON(SupervisorFrameKind.Error, { code: "version_mismatch", message: "protocol" });
          return;
        }
        if (hello.state_digest !== (behavior.expectDigest ?? "digest-1")) {
          this.serverJSON(SupervisorFrameKind.Error, { code: "state_mismatch", message: "state" });
          return;
        }
        this.serverJSON(SupervisorFrameKind.HelloAck, { version: SUPERVISOR_PROTOCOL_VERSION });
        return;
      }
      case SupervisorFrameKind.Create: {
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        lastCreateId = msg.id;
        const info = { ...INFO, id: msg.id };
        if (behavior.createDefer) {
          // 服务端按序处理: create 响应 (Created) 未发出前, 后续帧的响应都排在
          // 它之后; 这里延迟期间不响应后续帧, Created 延迟发出 (客户端可能已关闭)
          this.createPending = true;
          setTimeout(() => {
            this.createPending = false;
            this.serverJSON(SupervisorFrameKind.Created, info);
          }, 0);
          return;
        }
        this.serverJSON(SupervisorFrameKind.Created, info);
        return;
      }
      case SupervisorFrameKind.Attach: {
        if (behavior.attachError) {
          this.serverJSON(SupervisorFrameKind.Error, { code: behavior.attachError, message: behavior.attachError });
          return;
        }
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        if (msg.id !== lastCreateId && msg.id !== INFO.id) {
          this.serverJSON(SupervisorFrameKind.Error, { code: "not_found", message: "not found" });
          return;
        }
        this.serverJSON(SupervisorFrameKind.Attached, { ...INFO, id: msg.id });
        return;
      }
      case SupervisorFrameKind.Input:
        this.serverJSON(SupervisorFrameKind.InputAck, { written: frame.payload.length });
        return;
      case SupervisorFrameKind.KillSession:
        if (behavior.killSessionError) {
          this.serverJSON(SupervisorFrameKind.Error, { code: behavior.killSessionError, message: "reap failed" });
          return;
        }
        // create 响应在途: killSession 的 OK 按序排在 Created 之后, 客户端已关闭读不到
        if (this.createPending) return;
        this.serverJSON(SupervisorFrameKind.OK, {});
        return;
      case SupervisorFrameKind.Resize:
      case SupervisorFrameKind.Kill:
      case SupervisorFrameKind.Detach:
        this.serverJSON(SupervisorFrameKind.OK, {});
        return;
      default:
        this.serverJSON(SupervisorFrameKind.Error, { code: "protocol", message: `unexpected ${frame.kind}` });
    }
  }

  private emit(type: string, event: unknown): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

function factory(url: string): WebSocket {
  return new FakeWebSocket(url) as unknown as WebSocket;
}

function tick(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  FakeWebSocket.reset();
  behavior.helloError = undefined;
  behavior.attachError = undefined;
  behavior.expectDigest = undefined;
  behavior.helloErrorFrom = undefined;
  behavior.helloHangFrom = undefined;
  behavior.createDefer = undefined;
  behavior.killSessionError = undefined;
  lastCreateId = "s-1";
});

describe("supervisor 帧编解码", () => {
  it("帧头为 4 字节大端长度 + 1 字节类型", () => {
    const frame = encodeSupervisorFrame(9, new Uint8Array([1, 2, 3]));
    expect([...frame]).toEqual([0, 0, 0, 3, 9, 1, 2, 3]);
  });

  it("拒绝超过 256KiB 的 payload", () => {
    expect(() => encodeSupervisorFrame(1, new Uint8Array(SUPERVISOR_MAX_FRAME_PAYLOAD + 1))).toThrow(
      DeviceTerminalError,
    );
  });

  it("解析器处理拆包: 逐字节喂入也能还原完整帧", () => {
    const parser = new SupervisorFrameParser();
    const encoded = encodeSupervisorJSONFrame(SupervisorFrameKind.Created, INFO);
    const frames: SupervisorFrame[] = [];
    for (const byte of encoded) frames.push(...parser.push(new Uint8Array([byte])));
    expect(frames).toHaveLength(1);
    expect(frames[0].kind).toBe(SupervisorFrameKind.Created);
    expect(JSON.parse(new TextDecoder().decode(frames[0].payload))).toEqual(INFO);
  });

  it("解析器处理粘包: 一次喂入多帧按序还原", () => {
    const parser = new SupervisorFrameParser();
    const merged = new Uint8Array([
      ...encodeSupervisorJSONFrame(SupervisorFrameKind.HelloAck, { version: 2 }),
      ...encodeSupervisorFrame(SupervisorFrameKind.Output, encodeSupervisorOutputPayload(0n, new Uint8Array([65]))),
      ...encodeSupervisorJSONFrame(SupervisorFrameKind.Exit, { code: 0 }),
    ]);
    const frames = parser.push(merged);
    expect(frames.map((f) => f.kind)).toEqual([
      SupervisorFrameKind.HelloAck,
      SupervisorFrameKind.Output,
      SupervisorFrameKind.Exit,
    ]);
  });

  it("解析器拒绝超长 payload 声明", () => {
    const parser = new SupervisorFrameParser();
    const header = new Uint8Array(5);
    new DataView(header.buffer).setUint32(0, SUPERVISOR_MAX_FRAME_PAYLOAD + 1);
    header[4] = SupervisorFrameKind.Output;
    expect(() => parser.push(header)).toThrow(/exceeds limit/);
  });

  it("output payload 为 8 字节大端 seq + 原始字节", () => {
    const payload = encodeSupervisorOutputPayload(7n, new Uint8Array([104, 105]));
    expect([...payload.slice(0, 8)]).toEqual([0, 0, 0, 0, 0, 0, 0, 7]);
    const parsed = parseSupervisorOutput(payload);
    expect(parsed.seq).toBe(7n);
    expect(new TextDecoder().decode(parsed.data)).toBe("hi");
    expect(() => parseSupervisorOutput(new Uint8Array(7))).toThrow(/too short/);
  });

  it("unixNanoFromRFC3339 保留纳秒精度并换算时区", () => {
    expect(unixNanoFromRFC3339("2026-10-07T05:34:56.123456789+08:00")).toBe(CREATED_AT_NS);
    expect(unixNanoFromRFC3339("2026-10-06T21:34:56.123456789Z")).toBe(CREATED_AT_NS);
    expect(unixNanoFromRFC3339("2026-10-06T21:34:56Z")).toBe(
      BigInt(Date.UTC(2026, 9, 6, 21, 34, 56) / 1000) * 1_000_000_000n,
    );
    expect(() => unixNanoFromRFC3339("not-a-time")).toThrow(DeviceTerminalError);
  });
});

describe("DeviceBridge hello 协商", () => {
  it("hello 携带 version 与 state digest, helloAck 完成协商", async () => {
    const promise = DeviceBridge.connect("d-1", "digest-1", { factory });
    const bridge = await promise;
    const ws = FakeWebSocket.instances[0];
    expect(ws.url).toContain("/fleet/devices/d-1/bridge");
    expect(ws.sentJSON(SupervisorFrameKind.Hello)).toEqual({
      version: SUPERVISOR_PROTOCOL_VERSION,
      state_digest: "digest-1",
    });
    bridge.detach();
  });

  it("version 不匹配回 version_mismatch 错误", async () => {
    behavior.helloError = "version_mismatch";
    await expect(DeviceBridge.connect("d-1", "digest-1", { factory })).rejects.toMatchObject({
      code: "version_mismatch",
    });
  });

  it("state digest 不匹配回 state_mismatch 错误", async () => {
    await expect(DeviceBridge.connect("d-1", "wrong-digest", { factory })).rejects.toMatchObject({
      code: "state_mismatch",
    });
  });
});

describe("DeviceBridge 会话流程", () => {
  it("create 发送稳定 id+attempt 与命令尺寸, 返回会话信息", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    const info = await bridge.create({ id: "s-1", attempt: "a-1", cols: 80, rows: 24 });
    expect(info).toEqual(INFO);
    expect(FakeWebSocket.instances[0].sentJSON(SupervisorFrameKind.Create)).toEqual({
      id: "s-1",
      attempt: "a-1",
      command: [],
      cols: 80,
      rows: 24,
    });
    bridge.detach();
  });

  it("attach 携带期望身份, expect_created_at_unix_nano 为精确整数字面量", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    const info = await bridge.attach("s-1", { createdAtUnixNano: CREATED_AT_NS, incarnation: "inc-1" });
    expect(info.id).toBe("s-1");
    expect(FakeWebSocket.instances[0].sentPayloadText(SupervisorFrameKind.Attach)).toBe(
      `{"id":"s-1","expect_incarnation":"inc-1","expect_created_at_unix_nano":${CREATED_AT_NS.toString()}}`,
    );
    bridge.detach();
  });

  it("output 按 seq 严格递增校验, 失配即 protocol 错误并断流", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    await bridge.attach("s-1");
    const ws = FakeWebSocket.instances[0];
    const outputs: Uint8Array[] = [];
    const closes: (DeviceTerminalError | null)[] = [];
    bridge.onOutput = (data) => outputs.push(data);
    bridge.onClose = (error) => closes.push(error);
    ws.serverOutput(0n, "abc");
    ws.serverOutput(4n, "def");
    await tick();
    expect(outputs).toHaveLength(1);
    expect(new TextDecoder().decode(outputs[0])).toBe("abc");
    expect(closes).toHaveLength(1);
    expect(closes[0]?.code).toBe("protocol");
    expect(closes[0]?.message).toContain("sequence 4");
  });

  it("exit 帧上报退出码, fatal 帧按 code 断流", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    await bridge.attach("s-1");
    const ws = FakeWebSocket.instances[0];
    const exits: { code: number | null; signal: string }[] = [];
    const closes: (DeviceTerminalError | null)[] = [];
    bridge.onExit = (exit) => exits.push(exit);
    bridge.onClose = (error) => closes.push(error);
    ws.serverJSON(SupervisorFrameKind.Exit, { code: 130, signal: "" });
    ws.serverJSON(SupervisorFrameKind.Fatal, { code: "unavailable", message: "device gone" });
    await tick();
    expect(exits).toEqual([{ code: 130, signal: "" }]);
    expect(closes).toHaveLength(1);
    expect(closes[0]?.code).toBe("unavailable");
  });

  it("生产 clean 1000 拆桥 (pipeBridge 任一侧结束) 也按 disconnected 断流", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    await bridge.attach("s-1");
    const ws = FakeWebSocket.instances[0];
    const closes: (DeviceTerminalError | null)[] = [];
    bridge.onClose = (error) => closes.push(error);
    // internal/fleet/server/ws.go pipeBridge: 任一侧结束都以 StatusNormalClosure
    // (1000, "bridge closed") 关闭用户 WS; 设备离线走的就是这条路径。
    ws.serverClose(1000, "bridge closed");
    await tick();
    expect(closes).toHaveLength(1);
    expect(closes[0]?.code).toBe("disconnected");
    expect(closes[0]?.message).toContain("1000");
  });

  it("exit 帧后的 clean 关闭是流的自然终结, 不再报 disconnected", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    await bridge.attach("s-1");
    const ws = FakeWebSocket.instances[0];
    const exits: { code: number | null; signal: string }[] = [];
    const closes: (DeviceTerminalError | null)[] = [];
    bridge.onExit = (exit) => exits.push(exit);
    bridge.onClose = (error) => closes.push(error);
    ws.serverJSON(SupervisorFrameKind.Exit, { code: 0 });
    ws.serverClose(1000, "bridge closed");
    await tick();
    expect(exits).toEqual([{ code: 0, signal: "" }]);
    expect(closes).toHaveLength(1);
    expect(closes[0]).toBeNull();
  });

  it("input 分片等待 inputAck, resize/kill 等 OK 响应", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    await bridge.attach("s-1");
    const ws = FakeWebSocket.instances[0];
    const written = await bridge.input(new TextEncoder().encode("ls -l\r"));
    expect(written).toBe(6);
    expect(new TextDecoder().decode(ws.sentFrames().find((f) => f.kind === SupervisorFrameKind.Input)?.payload)).toBe(
      "ls -l\r",
    );
    await bridge.resize(120, 40);
    expect(ws.sentJSON(SupervisorFrameKind.Resize)).toEqual({ cols: 120, rows: 40 });
    await bridge.kill();
    bridge.detach();
  });

  it("killSession 在独立控制桥接上按 id 结束会话", async () => {
    await killDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      sessionId: "s-1",
      identity: { createdAtUnixNano: CREATED_AT_NS, incarnation: "inc-1" },
      factory,
    });
    expect(FakeWebSocket.instances[0].sentPayloadText(SupervisorFrameKind.KillSession)).toBe(
      `{"id":"s-1","expect_incarnation":"inc-1","expect_created_at_unix_nano":${CREATED_AT_NS.toString()}}`,
    );
  });
});

describe("openDeviceTerminal / resumeDeviceTerminal", () => {
  it("create 与 attach 分走两条桥接, attach 成功后会话可用", async () => {
    const { info, identity, bridge } = await openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      factory,
    });
    expect(FakeWebSocket.instances).toHaveLength(2);
    // create 携带客户端生成的稳定 id+attempt (Go client Create 同一合同),
    // Created 回显同一 id, attach 复用该 id
    const creator = FakeWebSocket.instances[0];
    const createMsg = creator.sentJSON(SupervisorFrameKind.Create) as { id: string; attempt: string };
    expect(createMsg.id).toMatch(/^[0-9A-Za-z]{26}$/);
    expect(createMsg.attempt).toMatch(/^[0-9A-Za-z]{26}$/);
    expect(info.id).toBe(createMsg.id);
    expect(identity.incarnation).toBe("inc-1");
    expect(identity.createdAtUnixNano).toBe(CREATED_AT_NS);
    expect(FakeWebSocket.instances[1].sentJSON(SupervisorFrameKind.Attach)).toMatchObject({ id: createMsg.id });
    bridge.detach();
  });

  it("attach 失败时用 creator 旧连接 killSession 回收, 不新建 reaper WS", async () => {
    behavior.attachError = "identity";
    await expect(
      openDeviceTerminal({ deviceId: "d-1", stateDigest: "digest-1", cols: 80, rows: 24, factory }),
    ).rejects.toMatchObject({ code: "identity" });
    // 只有 creator + attach 两条连接; killSession 走 creator (instances[0])
    expect(FakeWebSocket.instances).toHaveLength(2);
    const creator = FakeWebSocket.instances[0];
    const createMsg = creator.sentJSON(SupervisorFrameKind.Create) as { id: string };
    expect(creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession)).toBe(true);
    expect(creator.sentJSON(SupervisorFrameKind.KillSession)).toEqual({
      id: createMsg.id,
      expect_incarnation: "inc-1",
      expect_created_at_unix_nano: Number(CREATED_AT_NS),
    });
  });

  it("attach connect 失败 (hello 被拒) 时同样用 creator 旧连接回收", async () => {
    // 第二条连接 (attach connect) hello 失败: 与账号切换后 401/403 同路径,
    // create 已成功, 回收必须走切换前已鉴权的 creator, 不新建 reaper WS。
    behavior.helloErrorFrom = 2;
    await expect(
      openDeviceTerminal({ deviceId: "d-1", stateDigest: "digest-1", cols: 80, rows: 24, factory }),
    ).rejects.toMatchObject({ code: "internal" });
    expect(FakeWebSocket.instances).toHaveLength(2);
    const creator = FakeWebSocket.instances[0];
    expect(creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession)).toBe(true);
  });

  it("creator 回收 killSession 失败时附加 reapError, 不掩盖原 attach 错误", async () => {
    behavior.attachError = "identity";
    behavior.killSessionError = "unavailable";
    const error = await openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      factory,
    }).catch((e: unknown) => e);
    expect(error).toMatchObject({ code: "identity" });
    expect(error).toMatchObject({ reapError: { code: "unavailable" } });
  });

  it("create 结果不确定时取消: 按 attempt 回收而非零回收", async () => {
    behavior.createDefer = true;
    const box: { cancel?: () => void } = {};
    const promise = openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      factory,
      onCancel: (fn) => {
        box.cancel = fn ?? undefined;
      },
    });
    while (FakeWebSocket.instances.length < 1) await tick();
    const creator = FakeWebSocket.instances[0];
    while (!creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.Create)) await tick();
    box.cancel?.();
    await expect(promise).rejects.toMatchObject({ code: "closed" });
    await tick();
    // 服务端可能已完成 create 而 Created 未交付 (服务端 context.Background
    // 完成, 回包 best-effort): 取消按 id+attempt 回收, 不是零回收
    const createMsg = creator.sentJSON(SupervisorFrameKind.Create) as { id: string; attempt: string };
    const reap = creator.sentJSON(SupervisorFrameKind.KillSession) as { id: string; attempt: string };
    expect(reap).toEqual({ id: createMsg.id, attempt: createMsg.attempt });
    expect(creator.readyState).toBe(3);
    expect(creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.Attach)).toBe(false);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("attach 连接 CONNECTING/hello 挂起: 取消关闭 attach 解锁, creator 完成 killSession, 两条 WS 均关闭", async () => {
    behavior.helloHangFrom = 2;
    const box: { cancel?: () => void } = {};
    const promise = openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      factory,
      onCancel: (fn) => {
        box.cancel = fn ?? undefined;
      },
    });
    while (FakeWebSocket.instances.length < 2) await tick();
    // create 已成功, 第二条 attach 连接在 hello 期挂起
    box.cancel?.();
    await expect(promise).rejects.toMatchObject({ code: "closed" });
    await tick();
    const creator = FakeWebSocket.instances[0];
    const createMsg = creator.sentJSON(SupervisorFrameKind.Create) as { id: string };
    expect(creator.sentJSON(SupervisorFrameKind.KillSession)).toEqual({
      id: createMsg.id,
      expect_incarnation: "inc-1",
      expect_created_at_unix_nano: Number(CREATED_AT_NS),
    });
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances.every((ws) => ws.readyState === 3)).toBe(true);
  });

  it("resume 用期望身份重attach, output 从 seq 0 重放 backlog", async () => {
    const promise = resumeDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      sessionId: "s-1",
      identity: { createdAtUnixNano: CREATED_AT_NS, incarnation: "inc-1" },
      factory,
    });
    const { bridge } = await promise;
    const ws = FakeWebSocket.instances[0];
    const outputs: string[] = [];
    bridge.onOutput = (data) => outputs.push(new TextDecoder().decode(data));
    ws.serverOutput(0n, "backlog-1\n");
    ws.serverOutput(10n, "live-2\n");
    await tick();
    expect(outputs).toEqual(["backlog-1\n", "live-2\n"]);
    expect(ws.sentJSON(SupervisorFrameKind.Attach)).toEqual({
      id: "s-1",
      expect_incarnation: "inc-1",
      expect_created_at_unix_nano: Number(CREATED_AT_NS),
    });
    bridge.detach();
  });

  it("resume 会话不存在时回 not_found", async () => {
    await expect(
      resumeDeviceTerminal({
        deviceId: "d-1",
        stateDigest: "digest-1",
        sessionId: "gone",
        identity: { createdAtUnixNano: 1n, incarnation: "x" },
        factory,
      }),
    ).rejects.toMatchObject({ code: "not_found" });
  });
});

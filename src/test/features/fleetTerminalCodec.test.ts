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
const behavior: { helloError?: string; attachError?: string; expectDigest?: string } = {};

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static reset(): void {
    FakeWebSocket.instances = [];
  }

  readonly url: string;
  readonly sent: Uint8Array[] = [];
  binaryType = "";
  readyState = 1;
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
        if (behavior.helloError) {
          this.serverJSON(SupervisorFrameKind.Error, { code: behavior.helloError, message: behavior.helloError });
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
      case SupervisorFrameKind.Create:
        this.serverJSON(SupervisorFrameKind.Created, INFO);
        return;
      case SupervisorFrameKind.Attach: {
        if (behavior.attachError) {
          this.serverJSON(SupervisorFrameKind.Error, { code: behavior.attachError, message: behavior.attachError });
          return;
        }
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        if (msg.id !== INFO.id) {
          this.serverJSON(SupervisorFrameKind.Error, { code: "not_found", message: "not found" });
          return;
        }
        this.serverJSON(SupervisorFrameKind.Attached, INFO);
        return;
      }
      case SupervisorFrameKind.Input:
        this.serverJSON(SupervisorFrameKind.InputAck, { written: frame.payload.length });
        return;
      case SupervisorFrameKind.Resize:
      case SupervisorFrameKind.Kill:
      case SupervisorFrameKind.KillSession:
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
  it("create 发送命令与尺寸, 返回会话信息", async () => {
    const bridge = await DeviceBridge.connect("d-1", "digest-1", { factory });
    const info = await bridge.create({ cols: 80, rows: 24 });
    expect(info).toEqual(INFO);
    expect(FakeWebSocket.instances[0].sentJSON(SupervisorFrameKind.Create)).toEqual({
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
    expect(info.id).toBe("s-1");
    expect(identity.incarnation).toBe("inc-1");
    expect(identity.createdAtUnixNano).toBe(CREATED_AT_NS);
    bridge.detach();
  });

  it("attach 失败时在回收桥接上 killSession 新会话并抛出原错误", async () => {
    behavior.attachError = "identity";
    await expect(
      openDeviceTerminal({ deviceId: "d-1", stateDigest: "digest-1", cols: 80, rows: 24, factory }),
    ).rejects.toMatchObject({ code: "identity" });
    while (FakeWebSocket.instances.length < 3) await tick();
    const reaper = FakeWebSocket.instances[2];
    await tick();
    expect(reaper.sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession)).toBe(true);
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

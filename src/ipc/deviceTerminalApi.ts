// 设备远程终端的浏览器端 supervisor v2 客户端 (FLEET156)。
// 二进制合同逐条对齐 internal/supervisor/protocol.go 与 server.go, 经 M136 的
// GET /fleet/devices/{id}/bridge 出站桥接字节透明传输 (二进制 WS 消息即字节流):
// 帧 = 4 字节大端长度 + 1 字节类型 + payload; 控制帧 payload 为 JSON, output 帧
// payload = 8 字节大端 seq + 原始终端字节。hello 携带 version=2 与设备端 state
// digest (sha256(identity\x00state_dir), 浏览器无法自算, 由设备列表下发);
// 不匹配分别回 version_mismatch / state_mismatch 错误帧并拆线。
// create/attach 与 killSession 各用一条独立桥接 (与 Go 客户端同一模型:
// 每条桥接一条连接); attach 后 output 从 seq 0 重放全量 backlog, 客户端按
// seq 严格递增校验, 失配即 protocol 错误 (与 Go Stream.readLoop 一致)。

import { wsUrl } from "./env";

export const SUPERVISOR_PROTOCOL_VERSION = 2;
export const SUPERVISOR_MAX_FRAME_PAYLOAD = 256 * 1024;

// newSupervisorId 生成服务端 ids.Valid 接受的会话 id/attempt: 26 位字母数字
// (internal/ids/ids.go)。create 必须携带客户端生成的稳定 id+attempt
// (internal/supervisor/client.go Create/ReconcileCreate 同一合同), Created
// 响应丢失时才能按 attempt 回收结果不确定的 create。
const SUPERVISOR_ID_ALPHABET = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

export function newSupervisorId(): string {
  const bytes = new Uint8Array(26);
  crypto.getRandomValues(bytes);
  let id = "";
  for (const byte of bytes) id += SUPERVISOR_ID_ALPHABET[byte % SUPERVISOR_ID_ALPHABET.length];
  return id;
}

export const SupervisorFrameKind = {
  Hello: 1,
  HelloAck: 2,
  Error: 3,
  Create: 4,
  Created: 5,
  List: 6,
  Listed: 7,
  Attach: 8,
  Attached: 9,
  Input: 10,
  InputAck: 11,
  Resize: 12,
  OK: 13,
  Kill: 14,
  GetVersions: 15,
  Versions: 16,
  SetVersions: 17,
  Detach: 18,
  Output: 19,
  Exit: 20,
  Fatal: 21,
  KillSession: 22,
} as const;

export class DeviceTerminalError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "DeviceTerminalError";
  }
}

export interface SupervisorSessionInfo {
  id: string;
  created_at: string;
  incarnation: string;
  cols: number;
  rows: number;
  dead: boolean;
  exit_code?: number;
  signal?: string;
  finished_at?: string;
}

export interface SupervisorSessionIdentity {
  createdAtUnixNano: bigint;
  incarnation: string;
}

export interface SupervisorExit {
  code: number | null;
  signal: string;
}

interface ErrorMsg {
  code: string;
  message: string;
}

export function encodeSupervisorFrame(kind: number, payload: Uint8Array): Uint8Array {
  if (payload.length > SUPERVISOR_MAX_FRAME_PAYLOAD) {
    throw new DeviceTerminalError("protocol", `frame payload of ${payload.length} bytes exceeds limit`);
  }
  const frame = new Uint8Array(5 + payload.length);
  new DataView(frame.buffer).setUint32(0, payload.length);
  frame[4] = kind;
  frame.set(payload, 5);
  return frame;
}

export function encodeSupervisorJSONFrame(kind: number, message: unknown): Uint8Array {
  return encodeSupervisorFrame(kind, new TextEncoder().encode(JSON.stringify(message)));
}

export interface SupervisorFrame {
  kind: number;
  payload: Uint8Array;
}

// SupervisorFrameParser 把桥接字节流 (任意切分/粘包的 WS 二进制消息) 还原成帧;
// 超长 payload 与截断头一样按 protocol 错误处理 (对齐 readFrame 的上限校验)。
export class SupervisorFrameParser {
  private buffer = new Uint8Array(0);
  private view = new DataView(this.buffer.buffer);

  push(chunk: Uint8Array): SupervisorFrame[] {
    if (chunk.length > 0) {
      const merged = new Uint8Array(this.buffer.length + chunk.length);
      merged.set(this.buffer, 0);
      merged.set(chunk, this.buffer.length);
      this.buffer = merged;
      this.view = new DataView(this.buffer.buffer);
    }
    const frames: SupervisorFrame[] = [];
    for (;;) {
      if (this.buffer.length < 5) return frames;
      const length = this.view.getUint32(0);
      if (length > SUPERVISOR_MAX_FRAME_PAYLOAD) {
        throw new DeviceTerminalError("protocol", `frame payload of ${length} bytes exceeds limit`);
      }
      if (this.buffer.length < 5 + length) return frames;
      frames.push({ kind: this.buffer[4], payload: this.buffer.slice(5, 5 + length) });
      this.buffer = this.buffer.slice(5 + length);
      this.view = new DataView(this.buffer.buffer);
    }
  }
}

export function encodeSupervisorOutputPayload(seq: bigint, data: Uint8Array): Uint8Array {
  const payload = new Uint8Array(8 + data.length);
  new DataView(payload.buffer).setBigUint64(0, seq);
  payload.set(data, 8);
  return payload;
}

export function parseSupervisorOutput(payload: Uint8Array): { seq: bigint; data: Uint8Array } {
  if (payload.length < 8) {
    throw new DeviceTerminalError("protocol", `output frame of ${payload.length} bytes is too short`);
  }
  return { seq: new DataView(payload.buffer, payload.byteOffset).getBigUint64(0), data: payload.slice(8) };
}

// unixNanoFromRFC3339 把 Go time.Time JSON (RFC3339Nano, 可能带数值时区偏移)
// 转成 Unix 纳秒 BigInt。JS number 装不下纳秒 (2^53 < 2^63), 必须走 BigInt;
// Date.parse 只保留毫秒, 也不够。attach/killSession 的 expect_created_at_unix_nano
// 需要与设备端 time.Unix(0, ns) 精确相等 (sameIdentity 用 CreatedAt.Equal)。
export function unixNanoFromRFC3339(value: string): bigint {
  const match = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(Z|z|[+-]\d{2}:\d{2})$/.exec(
    value.trim(),
  );
  if (!match) {
    throw new DeviceTerminalError("protocol", `invalid RFC3339 timestamp: ${value}`);
  }
  const [, year, month, day, hour, minute, second, fraction = "", offset] = match;
  let seconds = Date.UTC(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute), Number(second)) / 1000;
  if (offset !== "Z" && offset !== "z") {
    const sign = offset[0] === "+" ? 1 : -1;
    seconds -= sign * (Number(offset.slice(1, 3)) * 3600 + Number(offset.slice(4, 6)) * 60);
  }
  return BigInt(seconds) * 1_000_000_000n + BigInt((fraction + "000000000").slice(0, 9));
}

// identityJSON 生成 attach/killSession 的 payload; expect_created_at_unix_nano
// 以原始整数字面量拼接 (JSON number 精度对 Go int64 解码无损, JSON.stringify
// 无法序列化 BigInt)。
function identityJSON(id: string, expect?: SupervisorSessionIdentity): string {
  const fields: string[] = [`"id":${JSON.stringify(id)}`];
  if (expect && expect.incarnation) fields.push(`"expect_incarnation":${JSON.stringify(expect.incarnation)}`);
  if (expect && expect.createdAtUnixNano !== 0n) {
    fields.push(`"expect_created_at_unix_nano":${expect.createdAtUnixNano.toString()}`);
  }
  return `{${fields.join(",")}}`;
}

export interface DeviceBridgeHandlers {
  onOutput?: (data: Uint8Array) => void;
  onExit?: (exit: SupervisorExit) => void;
  onClose?: (error: DeviceTerminalError | null) => void;
}

type WebSocketFactory = (url: string) => WebSocket;

const defaultWebSocketFactory: WebSocketFactory = (url) => new WebSocket(url);

// DeviceBridge 是一条桥接连接上的 supervisor 协议会话。生命周期:
// connect (hello 协商) -> create 或 attach (流式) -> input/resize/kill -> detach。
// 同一时刻只允许一个未完成请求 (对齐 Go clientConn 的串行语义); attach 后
// output/exit/fatal 与响应帧交错, 由读循环按类型分发。
export class DeviceBridge {
  private socket: WebSocket;
  private parser = new SupervisorFrameParser();
  private pending: {
    resolve: (frame: SupervisorFrame) => void;
    reject: (error: DeviceTerminalError) => void;
  }[] = [];
  private requestChain: Promise<unknown> = Promise.resolve();
  private attached = false;
  private exited = false;
  private expectedSeq = 0n;
  private closed = false;
  private failure: DeviceTerminalError | null = null;
  private detachSent = false;

  onOutput: ((data: Uint8Array) => void) | null = null;
  onExit: ((exit: SupervisorExit) => void) | null = null;
  onClose: ((error: DeviceTerminalError | null) => void) | null = null;

  private constructor(socket: WebSocket) {
    this.socket = socket;
    socket.binaryType = "arraybuffer";
    socket.addEventListener("message", (event) => {
      let frames: SupervisorFrame[];
      try {
        frames = this.parser.push(new Uint8Array(event.data as ArrayBuffer));
      } catch (error) {
        this.fail(error as DeviceTerminalError);
        return;
      }
      for (const frame of frames) this.dispatch(frame);
    });
    socket.addEventListener("close", (event) => {
      if (this.closed) return;
      // 本地主动 close/detach 已由 closed 屏蔽; 到达这里的都是远端拆线。
      // 生产 pipeBridge 在任一侧桥接结束后统一以 1000 正常关闭用户 WS
      // (internal/fleet/server/ws.go), 设备离线/代理重启走的就是这条路径,
      // 因此无论 wasClean 都按 disconnected 处理并驱动 UI 断开态;
      // 会话自然结束由 exit 帧先行表达, 随后的关闭是流的自然终结, 不算断线。
      const error =
        this.failure ??
        (this.exited
          ? null
          : new DeviceTerminalError(
              "disconnected",
              `桥接连接断开 (code=${event.code}${event.reason ? `, reason=${event.reason}` : ""})`,
            ));
      this.finish(error);
    });
    socket.addEventListener("error", () => {
      // 浏览器 WS 握手失败 (401/403/网络) 只会走到 error+close(1006);
      // 细节以 close 事件为准, 这里不重复造错误。
    });
  }

  static connect(
    deviceId: string,
    stateDigest: string,
    options: { factory?: WebSocketFactory; bridgePath?: (deviceId: string) => string; onBridge?: (bridge: DeviceBridge) => void } = {},
  ): Promise<DeviceBridge> {
    const path = options.bridgePath
      ? options.bridgePath(deviceId)
      : `/fleet/devices/${encodeURIComponent(deviceId)}/bridge`;
    const socket = (options.factory ?? defaultWebSocketFactory)(wsUrl(path));
    const bridge = new DeviceBridge(socket);
    // onBridge 在 socket 建立 (含 CONNECTING/hello 阶段) 后立即回调,
    // 调用方从首条连接起就可取消/关闭, 不必等 connect 兑现。
    options.onBridge?.(bridge);
    // 浏览器 WebSocket 建立前 send 会抛 InvalidStateError; 等 open 再发 hello。
    // readyState 用数值判定: 测试 fake 不实现 WebSocket.OPEN 常量。
    const opened = new Promise<void>((resolve, reject) => {
      if (socket.readyState === 1) {
        resolve();
        return;
      }
      socket.addEventListener("open", () => resolve(), { once: true });
      socket.addEventListener(
        "error",
        () => reject(new DeviceTerminalError("disconnected", "桥接连接建立失败")),
        { once: true },
      );
      socket.addEventListener(
        "close",
        (event) =>
          reject(
            new DeviceTerminalError(
              "disconnected",
              `桥接连接在建立前关闭 (code=${(event as CloseEvent).code})`,
            ),
          ),
        { once: true },
      );
    });
    return opened
      .then(() =>
        bridge.request(SupervisorFrameKind.Hello, {
          version: SUPERVISOR_PROTOCOL_VERSION,
          state_digest: stateDigest,
        }),
      )
      .then((frame) => {
        if (frame.kind !== SupervisorFrameKind.HelloAck) {
          throw new DeviceTerminalError("protocol", `expected hello_ack, got frame ${frame.kind}`);
        }
        const ack = JSON.parse(new TextDecoder().decode(frame.payload)) as { version?: number };
        if (ack.version !== SUPERVISOR_PROTOCOL_VERSION) {
          throw new DeviceTerminalError("protocol", `server speaks protocol ${ack.version}`);
        }
        return bridge;
      })
      .catch((error) => {
        bridge.close();
        throw error;
      });
  }

  private dispatch(frame: SupervisorFrame): void {
    if (frame.kind === SupervisorFrameKind.Output) {
      let parsed: { seq: bigint; data: Uint8Array };
      try {
        parsed = parseSupervisorOutput(frame.payload);
      } catch (error) {
        this.fail(error as DeviceTerminalError);
        return;
      }
      if (parsed.seq !== this.expectedSeq) {
        this.fail(
          new DeviceTerminalError(
            "protocol",
            `output sequence ${parsed.seq} does not match expected ${this.expectedSeq}`,
          ),
        );
        return;
      }
      this.expectedSeq += BigInt(parsed.data.length);
      this.onOutput?.(parsed.data);
      return;
    }
    if (frame.kind === SupervisorFrameKind.Exit) {
      const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { code?: number; signal?: string };
      this.exited = true;
      this.onExit?.({ code: msg.code ?? null, signal: msg.signal ?? "" });
      return;
    }
    if (frame.kind === SupervisorFrameKind.Fatal) {
      const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as ErrorMsg;
      this.fail(new DeviceTerminalError(msg.code, msg.message));
      return;
    }
    const next = this.pending.shift();
    if (!next) {
      this.fail(new DeviceTerminalError("protocol", `unsolicited frame ${frame.kind}`));
      return;
    }
    if (frame.kind === SupervisorFrameKind.Error) {
      const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as ErrorMsg;
      next.reject(new DeviceTerminalError(msg.code, msg.message));
      return;
    }
    next.resolve(frame);
  }

  private fail(error: DeviceTerminalError): void {
    if (this.failure) return;
    this.failure = error;
    this.finish(error);
    try {
      this.socket.close();
    } catch {
    }
  }

  private finish(error: DeviceTerminalError | null): void {
    if (this.closed) return;
    this.closed = true;
    const pending = this.pending.splice(0);
    const failure = error ?? this.failure;
    for (const request of pending) {
      request.reject(failure ?? new DeviceTerminalError("closed", "桥接连接已关闭"));
    }
    this.onClose?.(error);
  }

  // request 发送一帧并等待响应; 错误帧规整为带 code 的 DeviceTerminalError
  // (version_mismatch/state_mismatch/not_found/identity/exited/unavailable 等)。
  request(kind: number, message: unknown): Promise<SupervisorFrame> {
    const run = this.requestChain.then(() => this.roundTrip(kind, message));
    this.requestChain = run.catch(() => undefined);
    return run;
  }

  private roundTrip(kind: number, message: unknown): Promise<SupervisorFrame> {
    if (this.closed) {
      return Promise.reject(this.failure ?? new DeviceTerminalError("closed", "桥接连接已关闭"));
    }
    // ArrayBuffer.isView 跨 realm 成立 (jsdom/node 的 Uint8Array 不同源), instanceof 会误判
    const payload = ArrayBuffer.isView(message)
      ? new Uint8Array(message.buffer, message.byteOffset, message.byteLength)
      : new TextEncoder().encode(JSON.stringify(message));
    return new Promise<SupervisorFrame>((resolve, reject) => {
      this.pending.push({ resolve, reject });
      try {
        this.socket.send(encodeSupervisorFrame(kind, payload));
      } catch (error) {
        this.pending.pop();
        reject(
          new DeviceTerminalError(
            "disconnected",
            `send frame: ${error instanceof Error ? error.message : String(error)}`,
          ),
        );
      }
    });
  }

  create(options: { id: string; attempt: string; cols: number; rows: number; command?: string[] }): Promise<SupervisorSessionInfo> {
    return this.request(SupervisorFrameKind.Create, {
      id: options.id,
      attempt: options.attempt,
      command: options.command ?? [],
      cols: options.cols,
      rows: options.rows,
    }).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.Created) {
        throw new DeviceTerminalError("protocol", `expected created, got frame ${frame.kind}`);
      }
      return JSON.parse(new TextDecoder().decode(frame.payload)) as SupervisorSessionInfo;
    });
  }

  attach(id: string, expect?: SupervisorSessionIdentity): Promise<SupervisorSessionInfo> {
    return this.request(
      SupervisorFrameKind.Attach,
      new TextEncoder().encode(identityJSON(id, expect)),
    ).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.Attached) {
        throw new DeviceTerminalError("protocol", `expected attached, got frame ${frame.kind}`);
      }
      this.attached = true;
      this.expectedSeq = 0n;
      return JSON.parse(new TextDecoder().decode(frame.payload)) as SupervisorSessionInfo;
    });
  }

  input(data: Uint8Array): Promise<number> {
    return this.request(SupervisorFrameKind.Input, data).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.InputAck) {
        throw new DeviceTerminalError("protocol", `expected input_ack, got frame ${frame.kind}`);
      }
      const ack = JSON.parse(new TextDecoder().decode(frame.payload)) as { written?: number };
      return ack.written ?? data.length;
    });
  }

  resize(cols: number, rows: number): Promise<void> {
    return this.request(SupervisorFrameKind.Resize, { cols, rows }).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.OK) {
        throw new DeviceTerminalError("protocol", `expected ok, got frame ${frame.kind}`);
      }
    });
  }

  // kill 结束 attach 的会话本身 (frame 14, 仅 attached 连接可用)。
  kill(): Promise<void> {
    return this.request(SupervisorFrameKind.Kill, {}).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.OK) {
        throw new DeviceTerminalError("protocol", `expected ok, got frame ${frame.kind}`);
      }
    });
  }

  // killSession 在独立控制桥接上按 id 结束会话 (frame 22, 未 attach 的连接)。
  killSession(id: string, expect?: SupervisorSessionIdentity): Promise<void> {
    return this.request(
      SupervisorFrameKind.KillSession,
      new TextEncoder().encode(identityJSON(id, expect)),
    ).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.OK) {
        throw new DeviceTerminalError("protocol", `expected ok, got frame ${frame.kind}`);
      }
    });
  }

  // reconcileCreate 按 id+attempt 回收结果不确定的 create (internal/supervisor/
  // client.go ReconcileCreate 同一合同): 服务端 killByAttempt 按 attempt 匹配,
  // 会话不存在 (create 未落地) 时按合同回 OK, 幂等可重入。
  reconcileCreate(id: string, attempt: string): Promise<void> {
    return this.request(SupervisorFrameKind.KillSession, { id, attempt }).then((frame) => {
      if (frame.kind !== SupervisorFrameKind.OK) {
        throw new DeviceTerminalError("protocol", `expected ok, got frame ${frame.kind}`);
      }
    });
  }

  // reapCreateByAttempt 在 create 响应未定时按 attempt 回收: 帧直接写 socket
  // (不过 requestChain, create 可能仍在等响应), 不等回复。WS 消息有序, 随后
  // close 不会丢帧; 服务端按序处理完 create 后处理 killSession, 不会留孤儿。
  reapCreateByAttempt(id: string, attempt: string): void {
    if (this.closed) return;
    try {
      this.socket.send(encodeSupervisorJSONFrame(SupervisorFrameKind.KillSession, { id, attempt }));
    } catch {
    }
  }

  // detach 发送 detach 帧后关闭; WS 消息有序, close 不会抢在 detach 之前到达对端。
  detach(): void {
    if (this.detachSent || this.closed) return;
    this.detachSent = true;
    try {
      this.socket.send(encodeSupervisorJSONFrame(SupervisorFrameKind.Detach, {}));
    } catch {
    }
    this.close();
  }

  close(): void {
    if (this.closed) {
      try {
        this.socket.close();
      } catch {
      }
      return;
    }
    this.closed = true;
    const pending = this.pending.splice(0);
    for (const request of pending) {
      request.reject(this.failure ?? new DeviceTerminalError("closed", "桥接连接已关闭"));
    }
    try {
      this.socket.close();
    } catch {
    }
    if (!this.failure) this.onClose?.(null);
  }

  get isAttached(): boolean {
    return this.attached;
  }

  get isClosed(): boolean {
    return this.closed;
  }
}

// openDeviceTerminal 在两条桥接上完成 create+attach (与 Go 客户端/acceptance
// 同一模型)。create 携带客户端生成的稳定 id+attempt (internal/supervisor/
// client.go 同一合同): creator 连接从建立起保留到 open 结束, create 成功后
// 任何失败 (含 attach connect 被 401/403 拒绝) 都用这条切换前已鉴权的连接
// killSession 回收; Created 因取消/断连未交付 (info=nil) 时不能推断未创建
// (服务端用 context.Background() 完成 create, 回包 best-effort), 按 attempt
// reconcile 回收; killSession 失败以 reapError 附加显式记录, 不掩盖原始错误。
// wire 在 attach 前调用 (挂 output/exit/close 回调): attach 一批准对端立即
// 开始重放 backlog, 同批到达的 output 帧不能丢。
// onCancel 在每条连接建立 (含 CONNECTING/hello 阶段) 后即登记取消函数,
// settled 后以 onCancel(null) 注销; connecting 取消直接关闭 creator (create
// 未发出, 无会话可回收), creating 取消按 attempt 管线化回收后关闭 creator,
// attaching 取消关闭 attach 解锁 await, creator 留给回收路径用完再关。
export async function openDeviceTerminal(options: {
  deviceId: string;
  stateDigest: string;
  cols: number;
  rows: number;
  command?: string[];
  factory?: WebSocketFactory;
  bridgePath?: (deviceId: string) => string;
  wire?: (bridge: DeviceBridge) => void;
  onCancel?: (cancel: (() => void) | null) => void;
}): Promise<{ info: SupervisorSessionInfo; identity: SupervisorSessionIdentity; bridge: DeviceBridge }> {
  const { deviceId, stateDigest, factory, bridgePath } = options;
  const sessionId = newSupervisorId();
  const attempt = newSupervisorId();
  let creator: DeviceBridge | null = null;
  let attach: DeviceBridge | null = null;
  let info: SupervisorSessionInfo | null = null;
  let identity: SupervisorSessionIdentity | null = null;
  let stage: "connecting" | "creating" | "attaching" = "connecting";
  let cancelled = false;
  let settled = false;
  let reaped = false;
  const cancel = () => {
    if (cancelled || settled) return;
    cancelled = true;
    if (stage === "connecting") {
      creator?.close();
    } else if (stage === "creating") {
      if (creator && !reaped) {
        reaped = true;
        creator.reapCreateByAttempt(sessionId, attempt);
      }
      creator?.close();
    } else {
      attach?.close();
    }
  };
  try {
    creator = await DeviceBridge.connect(deviceId, stateDigest, {
      factory,
      bridgePath,
      onBridge: (bridge) => {
        creator = bridge;
        options.onCancel?.(cancel);
      },
    });
    if (cancelled) throw new DeviceTerminalError("closed", "打开已取消");
    stage = "creating";
    info = await creator.create({ id: sessionId, attempt, cols: options.cols, rows: options.rows, command: options.command });
    identity = {
      createdAtUnixNano: unixNanoFromRFC3339(info.created_at),
      incarnation: info.incarnation,
    };
    if (cancelled) throw new DeviceTerminalError("closed", "打开已取消");
    stage = "attaching";
    attach = await DeviceBridge.connect(deviceId, stateDigest, {
      factory,
      bridgePath,
      onBridge: (bridge) => {
        attach = bridge;
      },
    });
    try {
      options.wire?.(attach);
      const attached = await attach.attach(sessionId, identity);
      if (cancelled) {
        attach.close();
        throw new DeviceTerminalError("closed", "打开已取消");
      }
      settled = true;
      options.onCancel?.(null);
      creator.detach();
      return { info: attached, identity, bridge: attach };
    } catch (error) {
      attach.close();
      throw error;
    }
  } catch (error) {
    if (creator && !reaped && !creator.isClosed) {
      try {
        if (info && identity) {
          await creator.killSession(info.id, identity);
        } else if (stage !== "connecting") {
          await creator.reconcileCreate(sessionId, attempt);
        }
      } catch (reapError) {
        (error as { reapError?: unknown }).reapError = reapError;
      }
    }
    creator?.detach();
    attach?.close();
    options.onCancel?.(null);
    throw error;
  }
}

// resumeDeviceTerminal 用期望身份重attach既有会话: 设备端校验 incarnation,
// 不匹配回 identity 错误; 成功后从 seq 0 重放 backlog, 断开期间的输出不丢。
// wire 在 attach 前调用, 原因同 openDeviceTerminal。
// onCancel 从 socket 建立 (含 CONNECTING/hello 阶段) 起登记取消函数, settled
// 后以 onCancel(null) 注销; 取消关闭连接解锁 connect/attach 的 await (resume
// 不创建会话, 取消只是断开替换连接, 既有会话仍可再次 resume)。
export async function resumeDeviceTerminal(options: {
  deviceId: string;
  stateDigest: string;
  sessionId: string;
  identity: SupervisorSessionIdentity;
  factory?: WebSocketFactory;
  bridgePath?: (deviceId: string) => string;
  wire?: (bridge: DeviceBridge) => void;
  onCancel?: (cancel: (() => void) | null) => void;
}): Promise<{ info: SupervisorSessionInfo; bridge: DeviceBridge }> {
  let bridge: DeviceBridge | null = null;
  let cancelled = false;
  let settled = false;
  const cancel = () => {
    if (cancelled || settled) return;
    cancelled = true;
    bridge?.close();
  };
  try {
    bridge = await DeviceBridge.connect(options.deviceId, options.stateDigest, {
      factory: options.factory,
      bridgePath: options.bridgePath,
      onBridge: (b) => {
        bridge = b;
        options.onCancel?.(cancel);
      },
    });
    if (cancelled) throw new DeviceTerminalError("closed", "resume 已取消");
    options.wire?.(bridge);
    const info = await bridge.attach(options.sessionId, options.identity);
    if (cancelled) {
      bridge.close();
      throw new DeviceTerminalError("closed", "resume 已取消");
    }
    settled = true;
    options.onCancel?.(null);
    return { info, bridge };
  } catch (error) {
    bridge?.close();
    options.onCancel?.(null);
    throw error;
  }
}

// killDeviceTerminal 在独立控制桥接上按 id+期望身份结束会话 (显式 kill 确认后调用)。
export async function killDeviceTerminal(options: {
  deviceId: string;
  stateDigest: string;
  sessionId: string;
  identity?: SupervisorSessionIdentity;
  factory?: WebSocketFactory;
  bridgePath?: (deviceId: string) => string;
}): Promise<void> {
  const bridge = await DeviceBridge.connect(options.deviceId, options.stateDigest, {
    factory: options.factory,
    bridgePath: options.bridgePath,
  });
  try {
    await bridge.killSession(options.sessionId, options.identity);
  } finally {
    bridge.detach();
  }
}

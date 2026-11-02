import type { RemoteDesktopPrepareResult } from "../../ipc/generated/types";

export type RemoteProtocol = "rdp" | "vnc";

export type RemoteStatus = "connecting" | "connected" | "disconnected";

// "fit" scales the desktop into the pane; a number is a fixed zoom factor (1 is 100 percent).
export type ScaleMode = "fit" | number;

export const FIXED_SCALES = [0.5, 0.75, 1, 1.25, 1.5, 2] as const;

// Desktop sizes the asset form and the toolbar offer; any other size is a custom one.
export const RESOLUTION_PRESETS = [
  { width: 1280, height: 720 },
  { width: 1366, height: 768 },
  { width: 1600, height: 900 },
  { width: 1920, height: 1080 },
  { width: 2560, height: 1440 },
  { width: 3840, height: 2160 },
] as const;

export type ResolutionMode = { kind: "follow" } | { kind: "fixed"; width: number; height: number };

// A modifier the toolbar or the key bar can hold.
export type ModifierKey = "ControlLeft" | "AltLeft" | "ShiftLeft" | "MetaLeft";

export const MODIFIER_KEYS: readonly ModifierKey[] = ["ControlLeft", "AltLeft", "ShiftLeft", "MetaLeft"];

export interface RemoteSecret {
  username: string;
  domain: string;
  password: string;
}

// What the engine learns from a failed connect or an ended session, before it is classified.
export interface EngineFailure {
  protocol: RemoteProtocol;
  // security: the protocol rejected the session during negotiation; closed: the transport ended;
  // connect: the handshake failed before a picture existed.
  stage: "security" | "connect" | "closed";
  message: string;
  clean?: boolean;
  wasConnected?: boolean;
  // IronRDP error kind (IronErrorKind) and RDCleanPath details.
  rdpKind?: number;
  httpStatus?: number;
  tlsAlert?: number;
  // Winsock code from an RDCleanPath error the relay produced (connection refused, timed out, host not found).
  wsaError?: number;
  // The engine's full raw text, kept for the details fold; `message` is a single cleaned-up line.
  detail?: string;
  // Application error code, when the failure came from an IPC call.
  code?: string;
}

export class RemoteEngineError extends Error {
  readonly failure: EngineFailure;
  constructor(failure: EngineFailure) {
    super(failure.message);
    this.name = "RemoteEngineError";
    this.failure = failure;
  }
}

export interface ClientCallbacks {
  onConnected: () => void;
  onDisconnected: (failure: EngineFailure | null) => void;
  // Text the remote side copied; delivered only when the user asked to fetch it.
  onRemoteClipboard?: (text: string) => void;
  onDesktopSize?: (width: number, height: number) => void;
}

export interface ClientOptions {
  prepared: RemoteDesktopPrepareResult;
  secret: RemoteSecret | null;
  viewOnly: boolean;
  clipboard: boolean;
  // The size the desktop should start with (RDP).
  initialSize: { width: number; height: number } | null;
}

export interface RemoteClient {
  readonly protocol: RemoteProtocol;
  readonly supportsResize: boolean;
  connect(): Promise<void>;
  disconnect(): void;
  setScale(mode: ScaleMode): void;
  setViewOnly(viewOnly: boolean): void;
  // Asks the server for a new desktop size and reports what came of it. VNC always answers "unavailable".
  resize(width: number, height: number): "applied" | "pending" | "unavailable" | "failed";
  focus(): void;
  blur(): void;
  sendKey(code: string, down: boolean): void;
  sendCombo(codes: readonly string[]): Promise<void>;
  sendText(text: string): void;
  releaseAllKeys(): void;
  // Both move text through the local clipboard; they resolve to false when there was nothing to move or the browser
  // refused clipboard access.
  sendClipboard(): Promise<boolean>;
  fetchClipboard(): Promise<boolean>;
  // The element that receives pointer events, for the touch layer.
  inputElement(): HTMLElement | null;
  // A small JPEG data URL of the current picture, or null when there is none yet.
  thumbnail(maxWidth: number): string | null;
  // The desktop size in pixels, 0 before the first frame.
  desktopSize(): { width: number; height: number };
}

export type ClientFactory = (stage: HTMLElement, options: ClientOptions, callbacks: ClientCallbacks) => Promise<RemoteClient>;

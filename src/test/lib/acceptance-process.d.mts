import type { ChildProcess } from "node:child_process";

export interface ViteProcessHandle {
  process: ChildProcess;
  port: number;
  origin: string;
  stop(): Promise<void>;
}

export interface StartViteOptions {
  root?: string;
  port?: number;
  readyTimeout?: number;
  viteBin?: string;
}

export function freePort(): Promise<number>;
export function waitHttp(url: string, child: ChildProcess | null | undefined, timeout?: number): Promise<Response>;
export function stopProcess(child: ChildProcess | null | undefined): void;
export function startVite(options?: StartViteOptions): Promise<ViteProcessHandle>;

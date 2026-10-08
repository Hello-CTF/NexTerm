// 与 src/test/nodeFs.d.ts、src/test/features/imagePasteHttp.d.ts 同一模式:
// tsconfig 不加载 @types/node, 这里声明 src/test/lib 生命周期与 watcher 测试用到的最小 node 类型面。
declare module "node:child_process" {
  export interface ChildProcess {
    pid?: number;
    exitCode: number | null;
    signalCode: string | null;
    spawnfile: string;
    stdout: { on(event: "data", listener: (chunk: string) => void): void } | null;
    kill(signal?: string): boolean;
    once(event: "exit", listener: (code: number | null, signal: string | null) => void): this;
    once(event: string, listener: () => void): this;
  }
  export function spawn(command: string, args: readonly string[], options: { stdio: [string, string, string] }): ChildProcess;
}

declare module "node:fs" {
  export function mkdtempSync(prefix: string): string;
  export function writeFileSync(path: string, data: string): void;
  export function mkdirSync(path: string, options: { recursive: boolean }): string | undefined;
  export function rmSync(path: string, options: { recursive: boolean; force: boolean }): void;
  export function existsSync(path: string): boolean;
}

declare module "node:net" {
  export interface Server {
    listen(port: number, host: string, callback: () => void): this;
    once(event: "error", listener: (error: Error) => void): this;
    close(callback?: () => void): this;
  }
  export interface Socket {
    once(event: "connect", listener: () => void): this;
    once(event: "error", listener: (error: Error) => void): this;
    destroy(): void;
  }
  export function createServer(): Server;
  export function connect(options: { host: string; port: number }): Socket;
}

declare module "node:os" {
  export function tmpdir(): string;
}

declare module "node:path" {
  export function join(...parts: string[]): string;
  export function resolve(...parts: string[]): string;
  export function dirname(path: string): string;
}

declare module "node:url" {
  export function fileURLToPath(url: string): string;
}

declare const process: {
  execPath: string;
  env: Record<string, string | undefined>;
  kill(pid: number, signal?: string | number): void;
};

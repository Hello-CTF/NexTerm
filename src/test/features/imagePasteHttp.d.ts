// Same pattern as src/test/nodeFs.d.ts: tsconfig does not load @types/node,
// so this provides the minimal node:http type surface for the imagePasteHttp contract tests.
declare module "node:http" {
  export interface IncomingMessage {
    method?: string;
    url?: string;
    headers: Record<string, string | string[] | undefined>;
    on(event: "data", listener: (chunk: Uint8Array) => void): void;
    on(event: "end", listener: () => void): void;
  }
  export interface ServerResponse {
    setHeader(name: string, value: string): void;
    writeHead(status: number, headers?: Record<string, string>): ServerResponse;
    end(body?: string): void;
  }
  export interface Server {
    listen(port: number, host: string, callback: () => void): void;
    address(): { port: number; address: string } | string | null;
    close(callback: (err?: Error) => void): void;
  }
  export function createServer(
    handler: (req: IncomingMessage, res: ServerResponse) => void,
  ): Server;
}

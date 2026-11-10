declare module "node:fs" {
  export function readFileSync(path: string | URL, encoding: string): string;
  export function readdirSync(
    path: string | URL,
    options: { withFileTypes: true },
  ): { name: string; isDirectory(): boolean }[];
}

declare module "node:fs" {
  export function readFileSync(path: string | URL, encoding: string): string;
  export function readdirSync(
    path: string,
    options: { withFileTypes: true },
  ): { name: string; isDirectory(): boolean }[];
}

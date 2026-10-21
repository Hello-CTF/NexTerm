declare module "node:path" {
  export function resolve(...segments: string[]): string;
  const path: { resolve: typeof resolve };
  export default path;
}

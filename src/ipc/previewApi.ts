import { call } from "./commands";

export interface EmbedCheckResult {
  status: "embeddable" | "blocked" | "unreachable";
  reason?: string;
}

type UntypedCall = (cmd: string, args: Record<string, unknown>) => Promise<unknown>;

// preview_embed_check exists on the desktop app only. It is not in the generated manifest until the contract is
// regenerated, so the call goes through the untyped path.
export const previewApi = {
  embedCheck: (url: string) => (call as unknown as UntypedCall)("preview_embed_check", { url }) as Promise<EmbedCheckResult>,
};

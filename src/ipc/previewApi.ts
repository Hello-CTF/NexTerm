import { call } from "./commands";
import type { EmbedCheck } from "./generated/types";

export type EmbedCheckResult = Omit<EmbedCheck, "status"> & { status: "embeddable" | "blocked" | "unreachable" };

// preview_embed_check exists on the desktop app only.
export const previewApi = {
  embedCheck: async (url: string): Promise<EmbedCheckResult> => (await call("preview_embed_check", { url })) as EmbedCheckResult,
};

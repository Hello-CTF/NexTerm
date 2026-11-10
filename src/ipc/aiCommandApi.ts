import { call } from "./commands";
import type { AiCommandGenerateDTO, AiCommandGenerateRequest } from "./generated/types";

export type { AiCommandGenerateDTO, AiCommandGenerateRequest };

export const aiCommandApi = {
  // generate asks the active AI model for one shell command; the backend never runs it.
  generate: (request: AiCommandGenerateRequest) => call("ai_command_generate", { ...request }),
};

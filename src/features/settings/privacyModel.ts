// Display name of a local privacy model. Only one model ships today; a future model id maps to its own name here.
const DEFAULT_MODEL_NAME = "AstrLink Guard";
const MODEL_NAMES: Record<string, string> = {};

export function modelDisplayName(model?: object | null): string {
  const id = (model as { modelId?: string } | null | undefined)?.modelId;
  return (id && MODEL_NAMES[id]) || DEFAULT_MODEL_NAME;
}

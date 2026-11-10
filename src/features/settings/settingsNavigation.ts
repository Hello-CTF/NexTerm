// Settings show one group at a time, so a link to a section must go through
// the settings view: it opens the section's group, then scrolls to it. A link
// may also name one setting (its data-setting key), which the view reveals and
// highlights the way a settings search result does. A request made before the
// view mounts is delivered when it subscribes.
type Listener = (sectionId: string, itemId?: string) => void;

const listeners = new Set<Listener>();

function deliver(listener: Listener, sectionId: string, itemId?: string): void {
  if (itemId === undefined) listener(sectionId);
  else listener(sectionId, itemId);
}
let pending: { sectionId: string; itemId?: string } | null = null;

export function showSettingsSection(sectionId: string, itemId?: string): void {
  if (listeners.size === 0) {
    pending = { sectionId, itemId };
    return;
  }
  for (const listener of listeners) deliver(listener, sectionId, itemId);
}

export function onSettingsSection(listener: Listener): () => void {
  listeners.add(listener);
  if (pending !== null) {
    const request = pending;
    pending = null;
    deliver(listener, request.sectionId, request.itemId);
  }
  return () => {
    listeners.delete(listener);
  };
}

// Settings show one group at a time, so a link to a section must go through
// the settings view: it opens the section's group, then scrolls to it. A
// request made before the view mounts is delivered when it subscribes.
type Listener = (sectionId: string) => void;

const listeners = new Set<Listener>();
let pending: string | null = null;

export function showSettingsSection(sectionId: string): void {
  if (listeners.size === 0) {
    pending = sectionId;
    return;
  }
  for (const listener of listeners) listener(sectionId);
}

export function onSettingsSection(listener: Listener): () => void {
  listeners.add(listener);
  if (pending !== null) {
    const sectionId = pending;
    pending = null;
    listener(sectionId);
  }
  return () => {
    listeners.delete(listener);
  };
}

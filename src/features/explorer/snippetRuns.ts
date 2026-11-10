import { useSyncExternalStore } from "react";

// Multi-step snippet runs that wait for terminal output. They outlive the snippet panel, so their progress
// and cancel control are shown app-wide.

export interface SnippetRunView {
  id: number;
  name: string;
  step: number;
  total: number;
  // The pattern the run is waiting for, or null between waits.
  waitingFor: string | null;
  cancel: () => void;
}

let runs: readonly SnippetRunView[] = [];
let nextId = 1;
const listeners = new Set<() => void>();

function emit(next: readonly SnippetRunView[]) {
  runs = next;
  for (const listener of [...listeners]) listener();
}

export interface SnippetRunHandle {
  update: (patch: Partial<Pick<SnippetRunView, "step" | "waitingFor">>) => void;
  end: () => void;
}

export function beginSnippetRun(run: Omit<SnippetRunView, "id" | "step" | "waitingFor">): SnippetRunHandle {
  const id = nextId++;
  emit([...runs, { ...run, id, step: 0, waitingFor: null }]);
  return {
    update: (patch) => emit(runs.map((candidate) => (candidate.id === id ? { ...candidate, ...patch } : candidate))),
    end: () => emit(runs.filter((candidate) => candidate.id !== id)),
  };
}

export function snippetRuns(): readonly SnippetRunView[] {
  return runs;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useSnippetRuns(): readonly SnippetRunView[] {
  return useSyncExternalStore(subscribe, snippetRuns, snippetRuns);
}

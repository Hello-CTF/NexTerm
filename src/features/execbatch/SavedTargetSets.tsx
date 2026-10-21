import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { describeError } from "../../ui/errorText";

export interface SavedTargetSet {
  id: string;
  name: string;
  sessionIds: string[];
  createdAt: number;
  updatedAt: number;
}

// SavedTargetSetsApi is the data contract the picker needs. The live
// implementation is wired at the IPC boundary: target-set commands are not
// in the generated IPC manifest yet, so this component receives its API by
// injection instead of calling the command layer directly. Target sets are
// local-only in this version; nothing here syncs.
export interface SavedTargetSetsApi {
  list(): Promise<SavedTargetSet[]>;
  create(name: string, sessionIds: string[]): Promise<SavedTargetSet>;
  update(id: string, name: string, sessionIds: string[]): Promise<void>;
  delete(id: string): Promise<void>;
}

type savedTargetSetsState = {
  forApi: SavedTargetSetsApi | null;
  sets: SavedTargetSet[] | null;
};

export function SavedTargetSets({
  api,
  availableTargetIds,
  selectedIds,
  onSelect,
  disabled = false,
}: {
  api: SavedTargetSetsApi;
  availableTargetIds: readonly string[];
  selectedIds: readonly string[];
  onSelect: (sessionIds: string[]) => void;
  disabled?: boolean;
}) {
  const [state, setState] = useState<savedTargetSetsState>({ forApi: null, sets: null });
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingName, setEditingName] = useState("");
  const [armedDeleteId, setArmedDeleteId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const available = useMemo(() => new Set(availableTargetIds), [availableTargetIds]);
  // generationRef is the latest-request marker: list responses and failures
  // only commit while their generation is still current, so a slow response
  // from a previous API/owner or from before a mutation can never overwrite
  // newer state. apiRef carries the latest API identity so mutation
  // continuations can tell they no longer belong to the current owner.
  const generationRef = useRef(0);
  const apiRef = useRef(api);
  apiRef.current = api;

  const reload = useCallback(async () => {
    const generation = ++generationRef.current;
    const requestApi = api;
    try {
      const loaded = await requestApi.list();
      if (generationRef.current !== generation) return;
      setState({ forApi: requestApi, sets: Array.isArray(loaded) ? loaded : [] });
      setError(null);
    } catch (cause) {
      if (generationRef.current !== generation) return;
      setError(describeError(cause));
    }
  }, [api]);

  useEffect(() => {
    // A new API identity invalidates in-flight requests, clears stale
    // errors, and hides the previous owner's sets from the first frame
    // (the forApi render guard below withholds them synchronously).
    generationRef.current++;
    setState((current) => (current.forApi === api ? current : { forApi: null, sets: null }));
    setError(null);
    void reload();
  }, [api, reload]);

  const run = async (action: () => Promise<unknown>) => {
    const runApi = api;
    const runGeneration = generationRef.current;
    setBusy(true);
    setError(null);
    // Invalidate reads that started before the mutation so a late stale
    // list cannot wipe the item this mutation is about to save.
    generationRef.current++;
    try {
      await action();
      // The continuation belongs to the API that started the mutation. After
      // an API switch or a newer mutation it must not start a reload — the
      // old closure's reload would preempt the current owner's in-flight
      // list or hide it behind a permanent loading state.
      if (apiRef.current !== runApi || generationRef.current !== runGeneration + 1) {
        return;
      }
      await reload();
    } catch (cause) {
      if (apiRef.current !== runApi) {
        return;
      }
      setError(describeError(cause));
    } finally {
      setBusy(false);
    }
  };

  const trimmedName = name.trim();
  const save = () => {
    if (trimmedName === "" || selectedIds.length === 0) return;
    void run(async () => {
      await api.create(trimmedName, [...selectedIds]);
      setName("");
    });
  };

  const startRename = (set: SavedTargetSet) => {
    setEditingId(set.id);
    setEditingName(set.name);
  };

  const confirmRename = (set: SavedTargetSet) => {
    const trimmed = editingName.trim();
    if (trimmed === "") return;
    setEditingId(null);
    void run(() => api.update(set.id, trimmed, set.sessionIds));
  };

  const overwrite = (set: SavedTargetSet) => {
    if (selectedIds.length === 0) return;
    void run(() => api.update(set.id, set.name, [...selectedIds]));
  };

  const remove = (set: SavedTargetSet) => {
    if (armedDeleteId !== set.id) {
      setArmedDeleteId(set.id);
      return;
    }
    setArmedDeleteId(null);
    void run(() => api.delete(set.id));
  };

  const apply = (set: SavedTargetSet) => {
    onSelect(set.sessionIds.filter((id) => available.has(id)));
  };

  // The forApi guard withholds the previous owner's sets synchronously on
  // the first frame after an API switch, before any effect has run.
  const sets = state.forApi === api ? state.sets : null;

  return (
    <fieldset className="min-w-0" disabled={disabled}>
      <legend className="mb-1.5 text-[11px] text-neutral-500">保存的目标集（已选 {selectedIds.length}）</legend>
      {error !== null && (
        <div className="mb-2 text-[11px] text-amber-300" role="alert">
          {error}
        </div>
      )}
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <input
          className="nx-input nx-input-sm max-w-40"
          aria-label="目标集名称"
          placeholder="目标集名称"
          value={name}
          disabled={busy}
          onChange={(event) => setName(event.target.value)}
        />
        <button
          type="button"
          className="nx-link"
          disabled={busy || trimmedName === "" || selectedIds.length === 0}
          onClick={save}
        >
          保存当前选择
        </button>
        <span className="nx-hint">把已选目标存为可复用集合</span>
      </div>
      {sets === null ? (
        <div className="rounded border border-dashed border-neutral-800 px-3 py-3 text-[11px] text-neutral-500" role="status">
          加载中…
        </div>
      ) : sets.length === 0 ? (
        <div className="rounded border border-dashed border-neutral-800 px-3 py-3 text-[11px] text-neutral-500">
          暂无保存的目标集
        </div>
      ) : (
        <ul className="grid max-h-40 grid-cols-1 gap-1 overflow-y-auto rounded border border-neutral-800/70 p-2">
          {sets.map((set) => (
            <li key={set.id} className="flex min-w-0 flex-wrap items-center gap-2 px-1.5 py-1 text-[11px]">
              {editingId === set.id ? (
                <>
                  <input
                    className="nx-input nx-input-sm max-w-40"
                    aria-label={`重命名目标集 ${set.name}`}
                    value={editingName}
                    disabled={busy}
                    onChange={(event) => setEditingName(event.target.value)}
                  />
                  <button
                    type="button"
                    className="nx-link"
                    disabled={busy || editingName.trim() === ""}
                    onClick={() => confirmRename(set)}
                  >
                    保存名称
                  </button>
                  <button type="button" className="nx-link" onClick={() => setEditingId(null)}>
                    取消
                  </button>
                </>
              ) : (
                <>
                  <span className="truncate font-medium text-neutral-200" title={set.name}>
                    {set.name}
                  </span>
                  <span className="nx-hint">
                    在线 {set.sessionIds.filter((id) => available.has(id)).length}/{set.sessionIds.length}
                  </span>
                  <button
                    type="button"
                    className="nx-link"
                    aria-label={`选择目标集 ${set.name}`}
                    onClick={() => apply(set)}
                  >
                    选择
                  </button>
                  <button type="button" className="nx-link" disabled={busy} onClick={() => startRename(set)}>
                    重命名
                  </button>
                  <button
                    type="button"
                    className="nx-link"
                    disabled={busy || selectedIds.length === 0}
                    onClick={() => overwrite(set)}
                  >
                    覆盖
                  </button>
                  <button
                    type="button"
                    className="nx-link"
                    aria-label={`删除目标集 ${set.name}`}
                    disabled={busy}
                    onClick={() => remove(set)}
                  >
                    {armedDeleteId === set.id ? "确认删除" : "删除"}
                  </button>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
    </fieldset>
  );
}

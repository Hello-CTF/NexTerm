import {
  boundedSqlExecutionText,
  findConnectionWorkspace,
  getWorkspaceProblemResetGeneration,
  makeWorkspaceProblem,
  SQL_EXECUTION_HISTORY_LIMIT,
  WORKSPACE_PROBLEM_LIMIT,
  withoutConnectionProblem,
} from "./problems";
import type { SqlExecutionRecord, UiGet, UiSet, UiState } from "./types";

let workspaceGenerationSeq = 0;
let sqlExecutionSeq = 0;

export function createRuntimeSlice(set: UiSet, get: UiGet) {
  return {
    workspaceRuntime: {},
    workspaceRuntimeEpoch: 0,
    connectionProblems: {},
    getWorkspaceRuntime: (workspaceId) => get().workspaceRuntime[workspaceId],
    ensureWorkspaceRuntime: (workspaceId) => {
      const existing = get().workspaceRuntime[workspaceId];
      if (existing) return existing;
      if (!get().workspaces.some((workspace) => workspace.id === workspaceId)) return undefined;
      const generation = ++workspaceGenerationSeq;
      set((state) =>
        state.workspaceRuntime[workspaceId]
          ? state
          : {
              workspaceRuntime: {
                ...state.workspaceRuntime,
                [workspaceId]: { generation },
              },
            },
      );
      return get().workspaceRuntime[workspaceId];
    },
    updateSqlDraft: (workspaceId, draft, expectedGeneration) => {
      const current = get();
      if (!current.workspaces.some((w) => w.id === workspaceId)) return;
      const existing = current.workspaceRuntime[workspaceId];
      if (expectedGeneration !== undefined && existing?.generation !== expectedGeneration) return;
      const generation = existing?.generation ?? ++workspaceGenerationSeq;
      set({
        workspaceRuntime: {
          ...current.workspaceRuntime,
          [workspaceId]: { ...(existing ?? {}), generation, sqlDraft: draft },
        },
      });
    },
    recordSqlExecution: (workspaceId, record) => {
      set((state) => {
        const existing = state.workspaceRuntime[workspaceId];
        if (
          !state.workspaces.some((workspace) => workspace.id === workspaceId) ||
          !existing ||
          existing.generation !== record.generation
        ) {
          return state;
        }
        const sql = boundedSqlExecutionText(record.sql);
        const error = record.error === null ? null : boundedSqlExecutionText(record.error);
        const entry: SqlExecutionRecord = {
          ...record,
          id: ++sqlExecutionSeq,
          target: { ...record.target },
          sql: sql.text,
          sqlTruncated: sql.truncated,
          error: error?.text ?? null,
          errorTruncated: error?.truncated ?? false,
        };
        return {
          workspaceRuntime: {
            ...state.workspaceRuntime,
            [workspaceId]: {
              ...existing,
              sqlExecutionHistory: [entry, ...(existing.sqlExecutionHistory ?? [])].slice(
                0,
                SQL_EXECUTION_HISTORY_LIMIT,
              ),
            },
          },
        };
      });
    },
    recordWorkspaceProblem: (workspaceId, problem) => {
      set((state) => {
        const existing = state.workspaceRuntime[workspaceId];
        if (
          !state.workspaces.some((workspace) => workspace.id === workspaceId) ||
          !existing ||
          existing.generation !== problem.generation ||
          (problem.resetGeneration !== undefined &&
            problem.resetGeneration !== getWorkspaceProblemResetGeneration())
        ) {
          return state;
        }
        const entry = makeWorkspaceProblem(problem);
        return {
          workspaceRuntime: {
            ...state.workspaceRuntime,
            [workspaceId]: {
              ...existing,
              problems: [entry, ...(existing.problems ?? [])].slice(0, WORKSPACE_PROBLEM_LIMIT),
            },
          },
        };
      });
    },
    recordConnectionProblem: (problem) => {
      if (
        problem.resetGeneration !== undefined &&
        problem.resetGeneration !== getWorkspaceProblemResetGeneration()
      ) {
        return;
      }
      const workspace = findConnectionWorkspace(get().workspaces, problem);
      const generation = workspace
        ? get().ensureWorkspaceRuntime(workspace.id)?.generation
        : undefined;
      if (workspace && generation === undefined) return;
      const input = { ...problem, generation: generation ?? 0 };
      delete input.workspaceId;
      const entry = makeWorkspaceProblem(input);
      set((state) => {
        if (
          problem.resetGeneration !== undefined &&
          problem.resetGeneration !== getWorkspaceProblemResetGeneration()
        ) {
          return state;
        }
        const workspaceId = workspace && state.workspaces.some((item) => item.id === workspace.id)
          ? workspace.id
          : null;
        const workspaceRuntime = withoutConnectionProblem(state.workspaceRuntime, problem.connectionKey);
        const connectionProblems = { ...state.connectionProblems };
        delete connectionProblems[problem.connectionKey];
        if (workspaceId) {
          const existing = workspaceRuntime[workspaceId];
          if (!existing || existing.generation !== generation) return state;
          return {
            connectionProblems,
            workspaceRuntime: {
              ...workspaceRuntime,
              [workspaceId]: {
                ...existing,
                problems: [entry, ...(existing.problems ?? [])].slice(0, WORKSPACE_PROBLEM_LIMIT),
              },
            },
          };
        }
        connectionProblems[problem.connectionKey] = entry;
        return {
          connectionProblems: Object.fromEntries(
            Object.entries(connectionProblems)
              .sort(([, left], [, right]) => right.id - left.id)
              .slice(0, WORKSPACE_PROBLEM_LIMIT),
          ),
          workspaceRuntime,
        };
      });
    },
    clearConnectionProblem: (connectionKey) => {
      set((state) => {
        const workspaceRuntime = withoutConnectionProblem(state.workspaceRuntime, connectionKey);
        if (!state.connectionProblems[connectionKey] && workspaceRuntime === state.workspaceRuntime) {
          return state;
        }
        const connectionProblems = { ...state.connectionProblems };
        delete connectionProblems[connectionKey];
        return { connectionProblems, workspaceRuntime };
      });
    },
    dismissWorkspaceProblem: (workspaceId, problemId, expectedGeneration) => {
      const current = get();
      const existing = current.workspaceRuntime[workspaceId];
      if (!existing) return;
      if (expectedGeneration !== undefined && existing.generation !== expectedGeneration) return;
      set({
        workspaceRuntime: {
          ...current.workspaceRuntime,
          [workspaceId]: {
            ...existing,
            problems: (existing.problems ?? []).filter((problem) => problem.id !== problemId),
          },
        },
      });
    },
    clearSqlDraft: (workspaceId, expectedGeneration) => {
      const current = get();
      const existing = current.workspaceRuntime[workspaceId];
      if (!existing) return;
      if (expectedGeneration !== undefined && existing.generation !== expectedGeneration) return;
      set({
        workspaceRuntime: {
          ...current.workspaceRuntime,
          [workspaceId]: { ...existing, sqlDraft: undefined },
        },
      });
    },
  } satisfies Partial<UiState>;
}

export function nextWorkspaceGeneration(): number {
  return ++workspaceGenerationSeq;
}

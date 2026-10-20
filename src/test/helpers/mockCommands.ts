type CommandsModule = typeof import("../../ipc/commands");

/**
 * Deeply partial mock spec for the commands module: every export and every API
 * method is optional, but must exist on the real module so typos fail at
 * compile time and, for plain-JS drift, at mock construction time.
 */
export type CommandsMockSpec<M extends CommandsModule = CommandsModule> = {
  [K in keyof M]?: M[K] extends Record<string, unknown>
    ? { [P in keyof M[K]]?: M[K][P] }
    : M[K];
};

const PREFIX = "[mockCommands]";

function missingMock(path: string): () => never {
  return () => {
    throw new Error(
      `${PREFIX} ${path} is not mocked in this test; provide it in the vi.mock("../../ipc/commands") factory.`,
    );
  };
}

function isApiObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function buildApiMock(
  apiName: string,
  realApi: Record<string, unknown>,
  provided: Record<string, unknown> | undefined,
): Record<string, unknown> {
  if (provided) {
    for (const method of Object.keys(provided)) {
      if (!(method in realApi)) {
        throw new Error(`${PREFIX} mock spec mentions unknown method "${apiName}.${method}".`);
      }
    }
  }
  const mock: Record<string, unknown> = {};
  for (const method of Object.keys(realApi)) {
    const impl = provided?.[method];
    mock[method] = impl !== undefined ? impl : missingMock(`${apiName}.${method}()`);
  }
  return mock;
}

/**
 * Build a fully shaped stand-in for ../../ipc/commands from a partial spec.
 * Provided methods are used as-is; every other method (and every unprovided
 * top-level function) becomes a stub that throws a precise missing-mock error,
 * so a test that lets the code under test call an un-mocked API fails with the
 * exact `api.method` instead of a vague "is not a function" TypeError.
 *
 * Use inside a hoisted factory:
 *
 *   vi.mock("../../ipc/commands", async (importOriginal) => {
 *     const { createCommandsMock } = await import("../helpers/mockCommands");
 *     return createCommandsMock(await importOriginal(), {
 *       layoutApi: { get: mocks.layoutGet, put: mocks.layoutPut },
 *     });
 *   });
 */
export function createCommandsMock<M extends CommandsModule>(actual: M, spec: CommandsMockSpec<M>): M {
  const actualRecord = actual as Record<string, unknown>;
  const specRecord = spec as Record<string, Record<string, unknown> | undefined>;
  for (const key of Object.keys(specRecord)) {
    if (!(key in actualRecord)) {
      throw new Error(`${PREFIX} mock spec mentions unknown export "${key}".`);
    }
  }
  const result: Record<string, unknown> = {};
  for (const key of Object.keys(actualRecord)) {
    const realValue = actualRecord[key];
    const provided = specRecord[key];
    if (isApiObject(realValue)) {
      result[key] = buildApiMock(key, realValue, provided);
    } else if (provided !== undefined) {
      result[key] = provided;
    } else if (typeof realValue === "function") {
      result[key] = missingMock(`${key}()`);
    } else {
      result[key] = realValue;
    }
  }
  return result as M;
}

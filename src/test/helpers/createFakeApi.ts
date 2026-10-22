type CommandsModule = Partial<typeof import("../../ipc/commands")>;

type FakeValue<Value> = Value extends (...args: infer _Args) => infer _Result
  ? Value
  : Value extends object
    ? Partial<Value>
    : Value;

type FakeApi = {
  [Key in keyof CommandsModule]?: FakeValue<NonNullable<CommandsModule[Key]>>;
};

export function createFakeApi<T extends FakeApi>(fake: T): T {
  return fake;
}

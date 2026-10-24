import { useRef } from "react";

// Returns the previous object while the new one is structurally equal, so effects and memoized callbacks that
// depend on it do not re-run just because a parent rebuilt an identical value.
export function useStableValue<T>(value: T): T {
  const ref = useRef(value);
  if (ref.current !== value && JSON.stringify(ref.current) !== JSON.stringify(value)) ref.current = value;
  return ref.current;
}

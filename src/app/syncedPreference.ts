import {
  accountPreferencesReady,
  getAccountDefaults,
  getAccountOverrides,
  getAccountPreferenceStore,
  patchAccountOverrideCache,
  subscribeAccountOverrides,
  type AccountPreferenceStore,
} from "./preferences";

export interface SyncedPreferenceOptions<T> {
  /** The account preference key, declared in internal/account/preferences.go. */
  key: string;
  /** The device-local storage key used without a signed-in account. */
  localKey: string;
  /** Returns the value, or null when the stored value is not usable. */
  parse: (value: unknown) => T | null;
  fallback: () => T;
}

export interface SyncedPreference<T> {
  get(): T;
  /** Saves to the account when one is signed in, otherwise on this device. Rejects when the account write fails. */
  set(value: T): Promise<void>;
  /** Returns to the default: on the device the stored value is removed; an account stores the server default (or the builtin fallback) explicitly. */
  reset(): Promise<void>;
  subscribe(listener: () => void): () => void;
}

/**
 * A structured preference that follows the account when one is signed in and stays on the device otherwise.
 * Resolution order: account override > device value > server default > builtin fallback.
 *
 * A device value saved before the account had one is imported once: after the account preferences load and the
 * account has no value for the key, the device value is written to the account and the device copy is removed
 * only after that write succeeded. A failed import keeps the device value (still shown) and is retried on the
 * next account load.
 */
export function createSyncedPreference<T>(options: SyncedPreferenceOptions<T>): SyncedPreference<T> {
  const { key, localKey, parse, fallback } = options;
  const listeners = new Set<() => void>();
  let snapshot: T | undefined;
  let snapshotSignature = "";
  let importStore: AccountPreferenceStore | null = null;
  let writeSeq = 0;
  let pendingWrites = 0;
  let confirmedSeq = 0;
  let confirmed: { value: unknown } | null = null;
  let latestFailed = false;

  const readLocal = (): T | null => {
    try {
      const raw = localStorage.getItem(localKey);
      return raw === null ? null : parse(JSON.parse(raw));
    } catch {
      return null;
    }
  };

  const removeLocal = () => {
    try {
      localStorage.removeItem(localKey);
    } catch {
    }
  };

  const compute = (): T => {
    if (accountPreferencesReady()) {
      const overrides = getAccountOverrides();
      if (key in overrides) {
        const value = parse(overrides[key]);
        if (value !== null) return value;
      }
    }
    const local = readLocal();
    if (local !== null) return local;
    const defaults = getAccountDefaults();
    if (key in defaults) {
      const value = parse(defaults[key]);
      if (value !== null) return value;
    }
    return fallback();
  };

  // The device layer can change without an event (another pane, tests), so every read recomputes and the
  // previous object is returned while the value is unchanged, which keeps useSyncExternalStore stable.
  const get = (): T => {
    const next = compute();
    const signature = JSON.stringify(next);
    if (snapshot === undefined || signature !== snapshotSignature) {
      snapshot = next;
      snapshotSignature = signature;
    }
    return snapshot;
  };

  const notify = () => {
    get();
    for (const listener of listeners) listener();
  };

  const maybeImport = () => {
    const store = getAccountPreferenceStore();
    if (!store || !accountPreferencesReady() || importStore === store) return;
    if (key in getAccountOverrides()) return;
    const local = readLocal();
    if (local === null) return;
    importStore = store;
    store.putOverrides({ [key]: local }).then(
      () => {
        if (getAccountPreferenceStore() !== store) return;
        if (!(key in getAccountOverrides())) patchAccountOverrideCache({ [key]: local }, []);
        removeLocal();
        notify();
      },
      () => {
        // The device value stays in place and in use; the next account load tries again.
        if (importStore === store) importStore = null;
      },
    );
  };

  const set = async (value: T): Promise<void> => {
    const store = getAccountPreferenceStore();
    if (!store || !accountPreferencesReady()) {
      try {
        localStorage.setItem(localKey, JSON.stringify(value));
      } catch {
      }
      notify();
      return;
    }
    // Writes may overlap (A then B before A settles). A failure only rolls the cache back when it is the latest
    // write, and then to the newest value the account confirmed, so an older failure never undoes a newer write.
    const seq = ++writeSeq;
    if (pendingWrites === 0) {
      const overrides = getAccountOverrides();
      confirmed = key in overrides ? { value: overrides[key] } : null;
      confirmedSeq = seq - 1;
    }
    pendingWrites += 1;
    latestFailed = false;
    patchAccountOverrideCache({ [key]: value }, []);
    notify();
    const showConfirmed = () => {
      if (confirmed) patchAccountOverrideCache({ [key]: confirmed.value }, []);
      else patchAccountOverrideCache({}, [key]);
      notify();
    };
    try {
      await store.putOverrides({ [key]: value });
    } catch (error) {
      pendingWrites -= 1;
      if (seq === writeSeq && getAccountPreferenceStore() === store) {
        latestFailed = true;
        showConfirmed();
      }
      throw error;
    }
    pendingWrites -= 1;
    if (getAccountPreferenceStore() !== store) return;
    if (seq > confirmedSeq) {
      confirmedSeq = seq;
      confirmed = { value };
    }
    // The latest write failed earlier and showed an older value; this later-confirmed one is now the newest.
    if (latestFailed && seq === confirmedSeq) showConfirmed();
    // The account now holds the value, so a device copy left from before the account is no longer needed.
    removeLocal();
  };

  // Resetting an account saves the effective default explicitly (so an unimported device copy elsewhere cannot
  // replace it): the server default when one is set and usable, otherwise the builtin fallback. Without an account
  // the device value is removed.
  const reset = async (): Promise<void> => {
    if (getAccountPreferenceStore() && accountPreferencesReady()) {
      const defaults = getAccountDefaults();
      const serverDefault = key in defaults ? parse(defaults[key]) : null;
      await set(serverDefault ?? fallback());
      return;
    }
    removeLocal();
    notify();
  };

  const subscribe = (listener: () => void) => {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  };

  subscribeAccountOverrides(() => {
    notify();
    maybeImport();
  });
  maybeImport();

  return {
    get,
    set,
    reset,
    subscribe,
  };
}

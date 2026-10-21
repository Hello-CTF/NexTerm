import { useUi } from "../../app/store";
import { ForwardSettingsApiProvider } from "./forwardSettingsApi";
import { ForwardSettingsPanel } from "./ForwardSettingsPanel";
import { createForwardSettingsIpcApi } from "./forwardSettingsIpc";

// The adapter is a stateless set of typed IPC wrappers, so one instance is
// enough; the account reset comes from the key, not from the adapter.
const forwardSettingsIpcApi = createForwardSettingsIpcApi();

// Wires the production IPC adapter into the settings panel. The keyed
// provider remounts the panel on every account generation, so an account
// switch drops the previous account's rules immediately instead of showing
// stale state while the new account loads.
export function ForwardSettingsSection() {
  const accountEpoch = useUi((s) => s.accountEpoch);
  return (
    <ForwardSettingsApiProvider key={accountEpoch} api={forwardSettingsIpcApi}>
      <ForwardSettingsPanel />
    </ForwardSettingsApiProvider>
  );
}

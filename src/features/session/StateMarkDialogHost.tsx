import { lazy, useEffect, useState } from "react";
import { useUi } from "../../app/store";
import { Lazy } from "../../ui/LazyBoundary";
import { onStateMarkRequest, type StateMarkRequest } from "./stateMarkRequests";

const StateMarkDialog = lazy(() => import("./StateMarkDialog").then((m) => ({ default: m.StateMarkDialog })));

// StateMarkDialogHost opens the marking dialog for requests made through requestStateMark. A new request replaces
// the open one.
export function StateMarkDialogHost() {
  const [request, setRequest] = useState<StateMarkRequest | null>(null);
  const [generation, setGeneration] = useState(0);
  useEffect(
    () =>
      onStateMarkRequest((next) => {
        setRequest(next);
        setGeneration((value) => value + 1);
      }),
    [],
  );
  // The dialog goes away with its tab (closed, or an account switch reset the workspaces).
  const tabPresent = useUi((state) =>
    request
      ? state.workspaces.some((workspace) => workspace.panes.some((pane) => pane.tabs.some((tab) => tab.id === request.appTabId)))
      : false,
  );
  useEffect(() => {
    if (request && !tabPresent) setRequest(null);
  }, [request, tabPresent]);
  if (!request || !tabPresent) return null;
  return (
    <Lazy fallback="none" overlay>
      <StateMarkDialog key={generation} request={request} onClose={() => setRequest(null)} />
    </Lazy>
  );
}

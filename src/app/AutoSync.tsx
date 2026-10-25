import { useEffect } from "react";
import { useDesktopAutoSync, useWebAutoSync } from "../features/settings/useWebAutoSync";
import { replayPendingWebSyncRequest } from "./syncEvents";

// Loaded lazily: the auto-sync hooks pull in the whole sync console module.
export default function AutoSync() {
  useWebAutoSync();
  useDesktopAutoSync();
  useEffect(() => {
    replayPendingWebSyncRequest();
  }, []);
  return null;
}

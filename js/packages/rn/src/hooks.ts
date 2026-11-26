import { useContext, useEffect, useState } from "react";
import { KioskContext, type KioskContextValue } from "./provider.js";

export function useKiosk(): KioskContextValue {
  const ctx = useContext(KioskContext);
  if (!ctx) {
    throw new Error("useKiosk must be used inside <KioskProvider>");
  }
  return ctx;
}

export function useOperator() {
  const k = useKiosk();
  return {
    unlocked: k.operatorUnlocked,
    username: k.operatorUsername,
    login: k.loginOperator,
    logout: k.logoutOperator,
    patchConfig: k.patchConfig,
    triggerAction: k.triggerAction,
    deleteBoardRow: k.deleteBoardRow,
    fetchSubmissions: k.fetchSubmissions,
    fetchDevices: k.fetchDevices,
  };
}

/**
 * Returns a drift-corrected server clock ticking every intervalMs (default 250ms).
 */
export function useServerClock(intervalMs = 250): number {
  const { serverNowMs } = useKiosk();
  const [now, setNow] = useState<number>(() => serverNowMs());

  useEffect(() => {
    const id = setInterval(() => setNow(serverNowMs()), intervalMs);
    return () => clearInterval(id);
  }, [serverNowMs, intervalMs]);

  return now;
}

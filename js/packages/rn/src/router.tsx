import React, { useEffect } from "react";
import { ActivityIndicator, Platform, View } from "react-native";
import { useKiosk } from "./hooks.js";
import { OpColors } from "./operatorTheme.js";
import { ConnectScreen } from "./screens/ConnectScreen.js";
import { OperatorLoginModal } from "./screens/OperatorLoginModal.js";
import { OperatorScreen } from "./screens/OperatorScreen.js";

export interface KioskPhaseRouterProps {
  screens: Record<string, React.ComponentType>;
  fallbackScreen?: React.ComponentType;
  operatorExtraControls?: React.ReactNode;
}

export function KioskPhaseRouter({ screens, fallbackScreen, operatorExtraControls }: KioskPhaseRouterProps) {
  const { ready, paired, showConnect, showOperator, openOperator, closeOperator, operatorUnlocked, state } = useKiosk();

  // Desktop / Companion Web convenience shortcut: Ctrl+Shift+O (or Alt+Shift+O) toggles Operator panel from any phase.
  useEffect(() => {
    if (Platform.OS !== "web" || typeof window === "undefined") return;
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey || e.altKey) && e.shiftKey && e.key.toLowerCase() === "o") {
        e.preventDefault();
        if (showOperator) {
          closeOperator();
        } else {
          openOperator();
        }
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [showOperator, openOperator, closeOperator]);

  if (!ready) {
    return (
      <View style={{ flex: 1, backgroundColor: OpColors.ground, justifyContent: "center", alignItems: "center" }}>
        <ActivityIndicator color={OpColors.ink} size="large" />
      </View>
    );
  }

  if (!paired || showConnect) {
    return <ConnectScreen />;
  }

  if (showOperator) {
    return operatorUnlocked ? <OperatorScreen extraControls={operatorExtraControls} /> : <OperatorLoginModal />;
  }

  const phase = state.snapshot?.phase ?? "idle";
  const ScreenComp = screens[phase] ?? screens.idle ?? fallbackScreen;
  return ScreenComp ? <ScreenComp /> : null;
}

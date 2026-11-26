import React from "react";
import { ActivityIndicator, View } from "react-native";
import { useKiosk } from "./hooks.js";
import { ConnectScreen } from "./screens/ConnectScreen.js";
import { OperatorLoginModal } from "./screens/OperatorLoginModal.js";
import { OperatorScreen } from "./screens/OperatorScreen.js";

export interface KioskPhaseRouterProps {
  screens: Record<string, React.ComponentType>;
  fallbackScreen?: React.ComponentType;
  operatorExtraControls?: React.ReactNode;
}

export function KioskPhaseRouter({ screens, fallbackScreen, operatorExtraControls }: KioskPhaseRouterProps) {
  const { ready, paired, showConnect, showOperator, operatorUnlocked, state } = useKiosk();

  if (!ready) {
    return (
      <View style={{ flex: 1, backgroundColor: "#0F131A", justifyContent: "center", alignItems: "center" }}>
        <ActivityIndicator color="#38BDF8" size="large" />
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

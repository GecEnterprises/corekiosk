import { Platform } from "react-native";

/**
 * Tactile Field-Console tokens for @corekiosk/rn operator & setup screens.
 *
 * Intentionally avoids vibe-coded dark-slate/neon-cyan clichés (#0F172A + #38BDF8),
 * low-contrast slate-400 text, 11px tracked-out uppercase eyebrows, and nested border boxes.
 * Built for high-glare venue legibility and standing one-handed tablet ergonomics.
 */
export const OpColors = {
  ground: "#F2F0EB",        // Warm technical stone canvas
  surface: "#FFFFFF",       // Primary paper sheet
  recessed: "#E8E5DE",      // Recessed control track / well
  hover: "#DFDCD4",         // Pressed / subtle fill
  line: "#D6D2C8",          // Crisp structural hairline
  lineStrong: "#B8B3A6",    // Input & button border

  ink: "#161615",           // Carbon primary text (WCAG AAA on surface & ground)
  inkSecondary: "#52504A",  // Warm graphite secondary copy
  inkMuted: "#78756D",      // Subtle metadata / placeholders
  inkInverse: "#FFFFFF",    // Text on carbon / accent buttons

  accent: "#D94E1F",        // Braun industrial vermilion for primary commit actions
  accentSoft: "#FCEEE9",
  selectedBg: "#161615",    // Solid carbon for active tab / selected state
  selectedFg: "#FFFFFF",

  ok: "#15803D",
  okBg: "#DCFCE7",
  warn: "#B45309",
  warnBg: "#FEF3C7",
  danger: "#B91C1C",
  dangerBg: "#FEE2E2",
  dangerBorder: "#FCA5A5",
} as const;

export const OpMetrics = {
  radiusSm: 8,
  radiusMd: 10,
  radiusLg: 14,
  touchMin: 48,
} as const;

export const OpFonts = {
  sans: Platform.select({
    web: '"Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    default: undefined,
  }),
  mono: Platform.select({
    ios: "Menlo",
    android: "monospace",
    web: 'ui-monospace, "Cascadia Mono", "SFMono-Regular", Consolas, monospace',
    default: "monospace",
  }),
} as const;

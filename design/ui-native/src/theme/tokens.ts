// 由 design/scripts/sync-tokens.mjs 根据 design/tokens.json 生成，不要手改。

export const themes = ["light","dark"] as const;
export type LerrynTheme = (typeof themes)[number];

export const colors = {
  "light": {
    "kraft": "#3A5CF4",
    "kraft-strong": "#3345CF",
    "kraft-soft": "#D7DFEF",
    "kraft-edge": "#3A5CF4",
    "on-kraft": "#FFFFFF",
    "bark": "#245761",
    "paper": "#F5F1E6",
    "plate": "#fef8ef",
    "liner": "#F6F3EA",
    "liner-2": "#EAE9E2",
    "liner-3": "#E3E1D7",
    "ink": "#283E38",
    "ink-muted": "#57675F",
    "line": "#D6D6CB",
    "line-strong": "#57675F",
    "edge": "#D6D6CB",
    "run": "#006099",
    "run-soft": "#D6E1E3",
    "success": "#006D43",
    "success-soft": "#D7E3D2",
    "warning": "#9A6400",
    "on-warning": "#FFFFFF",
    "warning-ink": "#824D00",
    "warning-soft": "#E8DDCB",
    "danger": "#AD0025",
    "danger-soft": "#F6D6CE",
    "on-danger": "#FFFFFF",
    "danger-edge": "#D30033",
    "focus": "#3A5CF4",
    "backdrop": "rgba(11, 18, 36, 0.45)"
  },
  "dark": {
    "kraft": "#79A4FF",
    "kraft-strong": "#9EC3FF",
    "kraft-soft": "#1B253F",
    "kraft-edge": "#79A4FF",
    "on-kraft": "#0B0938",
    "bark": "#245761",
    "paper": "#0B1224",
    "plate": "#fef8ef",
    "liner": "#101729",
    "liner-2": "#131C2F",
    "liner-3": "#151C2F",
    "ink": "#F5F1E6",
    "ink-muted": "#A1A6B3",
    "line": "#1F2638",
    "line-strong": "#A1A6B3",
    "edge": "#1F2638",
    "run": "#72CFF8",
    "run-soft": "#16273D",
    "success": "#81D998",
    "success-soft": "#162933",
    "warning": "#E6BD41",
    "on-warning": "#230A00",
    "warning-ink": "#E6BD41",
    "warning-soft": "#252931",
    "danger": "#FFA5A7",
    "danger-soft": "#2A2333",
    "on-danger": "#2C0003",
    "danger-edge": "#FF747B",
    "focus": "#79A4FF",
    "backdrop": "rgba(11, 18, 36, 0.6)"
  }
} as const;
export type LerrynColor = keyof (typeof colors)['light'];

// Legacy depth names remain available for existing component callers.
export const flute = { E: 0, B: 0, C: 0, A: 0 } as const;
export type Flute = keyof typeof flute;

export const spacing = {
  "space-1": 4,
  "space-2": 8,
  "space-3": 12,
  "space-4": 16,
  "space-5": 20,
  "space-6": 24,
  "space-8": 32,
  "space-12": 48
} as const;

export const radius = {
  "radius-md": 4,
  "radius-lg": 6,
  "radius-xl": 8,
  "radius-2xl": 12,
  "radius-3xl": 16,
  "radius-4xl": 20,
  "radius-full": 9999
} as const;

export const fontFamily = {
  "display": "DMSans_700Bold",
  "display-semibold": "DMSans_600SemiBold",
  "display-heavy": "DMSans_800ExtraBold",
  "mono": "IBMPlexMono_400Regular",
  "mono-medium": "IBMPlexMono_500Medium"
} as const;

export const typeStyles = {
  "display-xl": {
    "family": "display",
    "fontSize": 40,
    "lineHeight": 44,
    "fontWeight": "800"
  },
  "display": {
    "family": "display",
    "fontSize": 32,
    "lineHeight": 38,
    "fontWeight": "700"
  },
  "title-1": {
    "family": "display",
    "fontSize": 24,
    "lineHeight": 30,
    "fontWeight": "700"
  },
  "title-2": {
    "family": "sans",
    "fontSize": 20,
    "lineHeight": 28,
    "fontWeight": "600"
  },
  "title-3": {
    "family": "sans",
    "fontSize": 17,
    "lineHeight": 24,
    "fontWeight": "600"
  },
  "body": {
    "family": "sans",
    "fontSize": 16,
    "lineHeight": 24,
    "fontWeight": "400"
  },
  "body-sm": {
    "family": "sans",
    "fontSize": 14,
    "lineHeight": 20,
    "fontWeight": "400"
  },
  "label": {
    "family": "sans",
    "fontSize": 13,
    "lineHeight": 18,
    "fontWeight": "600"
  },
  "caption": {
    "family": "sans",
    "fontSize": 12,
    "lineHeight": 16,
    "fontWeight": "500"
  },
  "mono": {
    "family": "mono",
    "fontSize": 13,
    "lineHeight": 20,
    "fontWeight": "500"
  },
  "mono-sm": {
    "family": "mono",
    "fontSize": 12,
    "lineHeight": 16,
    "fontWeight": "400"
  }
} as const;
export type LerrynTypeStyle = keyof typeof typeStyles;

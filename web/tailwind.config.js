/**
 * Tailwind theme is generated from web/src/lib/tokens.css — the single home for
 * raw values. Every color/font/radius/spacing entry references a CSS custom
 * property via var(); no hex lives here (hex gate), and arbitrary-value color
 * utilities (bg-[#…]) are forbidden by the same gate.
 * @type {import('tailwindcss').Config}
 */
export default {
  content: ["./src/**/*.{html,js,svelte,ts}"],
  theme: {
    // Replace the default palette entirely so only token-backed colors exist.
    colors: {
      transparent: "transparent",
      current: "currentColor",
      inherit: "inherit",

      "bg-page": "var(--bg-page)",
      "bg-0": "var(--bg-0)",
      "bg-1": "var(--bg-1)",
      "bg-2": "var(--bg-2)",
      "bg-3": "var(--bg-3)",
      "bg-4": "var(--bg-4)",
      "bg-5": "var(--bg-5)",
      "bg-film-1": "var(--bg-film-1)",
      "bg-film-2": "var(--bg-film-2)",
      "bg-film-flash": "var(--bg-film-flash)",

      "text-primary": "var(--text-primary)",
      "text-body": "var(--text-body)",
      "text-muted": "var(--text-muted)",
      "text-faint": "var(--text-faint)",
      "text-faintest": "var(--text-faintest)",
      "text-on-accent": "var(--text-on-accent)",
      "text-caption": "var(--text-caption)",

      accent: "var(--accent)",
      "accent-bright": "var(--accent-bright)",
      "accent-soft": "var(--accent-soft)",
      "accent-selection": "var(--accent-selection)",
      "accent-wash-08": "var(--accent-wash-08)",
      "accent-wash-12": "var(--accent-wash-12)",
      "accent-wash-14": "var(--accent-wash-14)",
      "accent-wash-16": "var(--accent-wash-16)",
      "accent-wash-18": "var(--accent-wash-18)",
      "accent-border": "var(--accent-border)",

      ok: "var(--ok)",
      warn: "var(--warn)",
      danger: "var(--danger)",
      "warn-underline": "var(--warn-underline)",
      "danger-wash": "var(--danger-wash)",

      "border-hairline": "var(--border-hairline)",
      "border-subtle": "var(--border-subtle)",
      "border-default": "var(--border-default)",
      "border-strong": "var(--border-strong)",
      "border-control": "var(--border-control)",
      "border-hover": "var(--border-hover)",
      "border-hover-control": "var(--border-hover-control)",

      "overlay-scrim": "var(--overlay-scrim)",
      "overlay-timecode": "var(--overlay-timecode)",
      "overlay-caption": "var(--overlay-caption)",
      "wave-unplayed": "var(--wave-unplayed)",
      "step-done": "var(--step-done)",
      "hover-row": "var(--hover-row)",
      "guide-dashed": "var(--guide-dashed)",
    },
    fontFamily: {
      ui: "var(--font-ui)",
      mono: "var(--font-mono)",
      fa: "var(--font-fa)",
    },
    borderRadius: {
      none: "0",
      1: "var(--radius-1)",
      2: "var(--radius-2)",
      3: "var(--radius-3)",
      4: "var(--radius-4)",
      full: "var(--radius-full)",
    },
    spacing: {
      0: "0",
      px: "1px",
      1: "var(--space-4)",
      1.5: "var(--space-6)",
      2: "var(--space-8)",
      2.5: "var(--space-10)",
      3: "var(--space-12)",
      3.5: "var(--space-14)",
      4: "var(--space-16)",
      4.5: "var(--space-18)",
      5: "var(--space-20)",
      6: "var(--space-24)",
    },
    extend: {
      height: {
        topbar: "var(--topbar-h)",
        statusbar: "var(--statusbar-h)",
        "statusbar-library": "var(--statusbar-h-library)",
      },
      transitionTimingFunction: {
        out: "var(--motion-ease)",
      },
      transitionDuration: {
        hover: "var(--motion-hover)",
        drawer: "var(--motion-drawer)",
        scrim: "var(--motion-scrim)",
      },
    },
  },
  plugins: [],
};

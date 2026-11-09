/**
 * Palette file: the single source of all colors. To reskin, change this (or write another Palette and point `palette` at it).
 *
 * The default follows Hyperledger Besu's brand colors: the logo teal as primary / accent, with grey, white and black neutrals.
 * Each mode (light / dark) has a complete set of roles; brand.ts only wires roles to UI tokens and holds no color values.
 *
 * Roles:
 * - accent: clickable interactions / primary buttons / selection / main chart line. base is for buttons and links (white text must pass WCAG AA),
 *   hover / active are one step darker for hover and press, bright is one step brighter (highlights, charts, dark backgrounds), soft is the tint.
 * - warning: needs attention but not an error yet (syncing, not applied, expiring soon), amber.
 * - success / danger / info / neutral: the other status colors (healthy, error, info, unknown), for status only, never used as accents.
 * - identity: tag colors for fixed categories (e.g. "Validator"); hues avoid teal and the status colors so they are not mistaken for status or clickable.
 */
export interface Tone { fg: string; bg: string }

export interface PaletteMode {
  // Neutral surfaces: background -> card -> secondary
  canvas: string;
  surface: string;
  surfaceElevated: string;
  // Structural lines
  borderHair: string;
  borderStrong: string;
  // Text: primary / secondary / muted / faintest (typographic aid, carries no information)
  textPrimary: string;
  textSecondary: string;
  textTertiary: string;
  textFaint: string;
  textOnAccent: string;
  textOnDark: string;
  // Primary accent
  accent: { base: string; hover: string; active: string; bright: string; soft: string };
  // Semantic status colors
  success: Tone;
  warning: Tone;
  danger: Tone;
  info: Tone;
  neutral: Tone;
  // Identity palette (non-semantic)
  identity: { indigo: Tone; violet: Tone; blue: Tone; slate: Tone };
}

export interface Palette {
  name: string;
  light: PaletteMode;
  dark: PaletteMode;
}

/** Besu brand colors: teal taken from the besu logo (around #2E9D9A); buttons on light backgrounds use one step darker so white text passes AA (≈4.8:1). */
export const besuPalette: Palette = {
  name: 'besu',
  light: {
    canvas: '#FAFBFC',
    surface: '#FFFFFF',
    surfaceElevated: '#F6F7F9',
    borderHair: '#E6E8EB',
    borderStrong: '#D4D8DD',
    textPrimary: '#14161A',
    textSecondary: '#454B53',
    textTertiary: '#6C727C',
    textFaint: '#B4B9C0',
    textOnAccent: '#FFFFFF',
    textOnDark: '#FFFFFF',
    accent: {
      base: '#287F7C', // buttons / links / selection (white text 4.8:1)
      hover: '#216B68',
      active: '#1A5755',
      bright: '#2E9D9A', // besu logo color: highlights, charts, icons
      soft: '#E3F3F2', // selected rows / tag background
    },
    success: { fg: '#26A05A', bg: '#E9F4EC' }, // one step brighter green so dots, bars and pills look lively; small text stays mostly plain
    warning: { fg: '#EA7A12', bg: '#FFE8D1' }, // bright orange (distinct from red, not brownish): about 3.2:1 on white, so only for pills, dots and bars; small text stays plain
    danger: { fg: '#B3322F', bg: '#FBE9E9' },
    info: { fg: '#1F6C9F', bg: '#E1F3FE' }, // deliberately distinct from accent: info = status, accent = interactive
    neutral: { fg: '#5B6068', bg: '#EFF1F3' },
    identity: {
      indigo: { fg: '#4338CA', bg: '#EEF2FF' },
      violet: { fg: '#6D28D9', bg: '#F5F3FF' },
      blue: { fg: '#1D4ED8', bg: '#DBEAFE' }, // was cyan, too close to the teal primary, so changed to blue
      slate: { fg: '#3A4A66', bg: '#E6ECF5' }, // blue-grey: for tags, distinct from plain grey without standing out
    },
  },
  dark: {
    canvas: '#0F1115',
    surface: '#16181D',
    surfaceElevated: '#1C1F26',
    borderHair: '#262A31',
    borderStrong: '#333842',
    textPrimary: '#E6E8EB',
    textSecondary: '#9AA0A8',
    // 5.25:1 on surface, 4.87:1 on surfaceElevated (2026-08 audit: the old value was only 3.34:1 on surfaceElevated)
    textTertiary: '#868C96',
    textFaint: '#565C65',
    textOnAccent: '#FFFFFF',
    textOnDark: '#FFFFFF',
    accent: {
      base: '#2E9D9A', // dark backgrounds use the logo color directly (5.5:1 on surface)
      hover: '#3DAFAB',
      active: '#257F7D',
      bright: '#5CC2BE',
      soft: '#11302F',
    },
    success: { fg: '#6CCB8A', bg: '#15251A' },
    warning: { fg: '#FFA24A', bg: '#3B1F0E' },
    danger: { fg: '#E07A77', bg: '#2A1716' },
    info: { fg: '#5BA6D8', bg: '#122430' },
    neutral: { fg: '#9AA0A8', bg: '#1C1F26' },
    identity: {
      indigo: { fg: '#A5B4FC', bg: '#1E2447' },
      violet: { fg: '#C4B5FD', bg: '#2A2050' },
      blue: { fg: '#93C5FD', bg: '#1B2A47' },
      slate: { fg: '#B7C3D6', bg: '#243044' },
    },
  },
};

/** The palette used site-wide. To swap it wholesale, write another Palette and point this line at it. */
export const palette: Palette = besuPalette;

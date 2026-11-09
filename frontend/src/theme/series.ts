// Categorical palette for cross-node overlay / split charts.
// Walk the hue wheel by the golden angle (137.5°) so nodes adjacent in the list get maximally different colors;
// fixed medium chroma / lightness keeps them in quiet tones that sit well with the brand color (not Grafana neon).
// Generates 30 colors -> one per node up to 30 nodes (cycles beyond that).
import { palette } from './palette';

function genPalette(n: number): string[] {
  const out = [palette.light.accent.bright]; // index 0 = primary accent (one step brighter so the line stands out)
  const golden = 137.508;
  for (let i = 1; i < n; i++) {
    const h = Math.round((i * golden) % 360);
    const s = 56 + (i % 3) * 6; // 56 / 62 / 68
    const l = 44 + (i % 2) * 8; // 44 / 52
    out.push(`hsl(${h}, ${s}%, ${l}%)`);
  }
  return out;
}

export const SERIES_PALETTE = genPalette(30);

export const seriesColor = (i: number) => SERIES_PALETTE[i % SERIES_PALETTE.length];

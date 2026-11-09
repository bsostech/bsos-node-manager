# Font Licenses

All fonts used by the web UI and the node onboarding runbook are licensed under the SIL Open Font License 1.1. This
directory holds the license texts so that they travel with the source. The root `NOTICE` file lists which font is
used where.

| File | Fonts | Source |
|---|---|---|
| `OFL-IBM-Plex.txt` | IBM Plex Sans, IBM Plex Mono | `LICENSE.txt` in the upstream IBM/plex repository |
| `OFL-Noto-Sans-TC.txt` | Noto Sans TC | `ofl/notosanstc/OFL.txt` in the upstream google/fonts repository. The `LICENSE` shipped with `@fontsource` uses a generic header, not this font's copyright line. |

The runbook embeds subsets of Noto Sans TC and IBM Plex Mono. A subset is a Modified Version under the OFL and may
not use a Reserved Font Name, so `tools/runbook/build.py` embeds them as `BSOS Sans TC` and `BSOS Mono` and copies
the copyright line from the first line of each file here.

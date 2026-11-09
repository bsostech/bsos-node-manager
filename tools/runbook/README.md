# Node Onboarding Runbook

This directory is the source of `frontend/src/runbook/runbook.zh.html` and `runbook.en.html`. When an operator
registers a node, the web UI fills the `{{...}}` placeholders with HTML-escaped values
(`frontend/src/lib/runbook.ts`) and offers the result as a download. Edit the Markdown here and regenerate; do not
edit the HTML directly.

| File | Purpose |
|---|---|
| `zh.md`, `en.md` | Content: front matter, `##` and `###` headings, a `>` line after a `##` heading (the section description), `:::note` blocks and `:::tabs` blocks (each `Label:` line followed by a code block becomes one tab) |
| `build.py` | Renders each Markdown file to a single offline HTML page: header with logo, sidebar table of contents, copy buttons on code blocks, tabs for the config formats. CSS, JavaScript, the logo and font subsets are inlined. |
| `assets/bsos-logo.svg` | Header logo |

## Regenerate

The build needs Python 3 with `markdown`, `beautifulsoup4`, `fontTools` and `brotli`, and `frontend/node_modules`
(run `npm ci` in `frontend/` first; the fonts come from `@fontsource/noto-sans-tc` and `@fontsource/ibm-plex-mono`).

```bash
python3 tools/runbook/build.py
```

Regenerate after every Markdown change: the embedded fonts contain only the characters that appear in the document,
so new text is otherwise missing glyphs. The subsets are Noto Sans TC 400 for body text, 500 for subheadings, bold
text and table headers, 700 for headings, and IBM Plex Mono 400 for code. They are embedded under renamed families with the OFL notice in a comment at the top of the file; see
`third_party/fonts/README.md` for why. Substituted values that contain Chinese characters not present in the template, such
as a node name, fall back to a system font.

## Placeholders

`{{NODE_ID}}`, `{{NODE_NAME}}`, `{{PLATFORM_URL}}`, `{{RPC_PORT}}`, `{{METRICS_PORT}}`, `{{PUBKEY_URL}}`,
`{{CONFIG_TOML}}`, `{{CONFIG_CLI}}`, `{{CONFIG_ENV}}`, `{{INSTALL_CMD}}`, `{{GENERATED_AT}}`

`CONFIG_*` and `INSTALL_CMD` appear inside `<pre><code>`. Unknown placeholders are left as they are. The join code
never goes into the runbook; `INSTALL_CMD` carries a placeholder (`<join-code>` in English) instead.

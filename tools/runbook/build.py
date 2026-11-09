#!/usr/bin/env python3
"""zh.md / en.md -> frontend/src/runbook/runbook.{zh,en}.html (single file, offline, font subsets embedded).

Page layout: sticky header with the logo top left, sidebar TOC, numbered sections,
copy buttons on code blocks, tabs to switch between the three config formats; when printing, the TOC and buttons are hidden and all tabs expanded.
See README.md in this directory for usage.

    python3 tools/runbook/build.py
"""
import base64
import io
import re
from pathlib import Path

import markdown
from bs4 import BeautifulSoup
from fontTools.ttLib import TTFont
from fontTools.subset import Subsetter, Options

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
FONTS = ROOT / "frontend/node_modules/@fontsource"
OUT = ROOT / "frontend/src/runbook"
ASCII = "".join(chr(c) for c in range(0x20, 0x7F))
# Chinese characters that appear in values the frontend substitutes in (# comment lines in config snippets, placeholders in the install command)
EXTRA_ZH = "RPC 認證帳戶白名單<接入碼>"
# Which elements each font weight is used in; the subset only includes characters that appear in those elements
# This HTML circulates on its own outside the repo, so the embedded fonts' license notice must travel with the file (OFL 1.1)
# The copyright lines are taken verbatim from the first line of the license files in third_party/fonts/, not rewritten
_first = lambda name: (ROOT / "third_party/fonts" / name).read_text().splitlines()[0].strip()
FONT_NOTICE = (f'Embedded fonts, licensed under the SIL Open Font License 1.1 (https://openfontlicense.org): '
               f'subset of Noto Sans TC, embedded as "BSOS Sans TC" - {_first("OFL-Noto-Sans-TC.txt")}; '
               f'subset of IBM Plex Mono, embedded as "BSOS Mono" - {_first("OFL-IBM-Plex.txt")}')
WEIGHTS = {500: ["h3", "th", "strong", ".kicker", ".toc", ".tab", ".copy", ".fq .k", "td.k"], 700: ["h1", "h2"]}

CSS = """
:root{
  --bg:#ffffff; --surface-2:#F5FBFF; --soft:#EDF6F9;
  --ink:#282D3C; --ink-2:#565E72; --muted:#6E7688;
  --line:#E3EAF0; --line-2:#CBD6E0;
  --brand:#00446A; --brand-soft:#DEF0FD;
  --radius-sm:10px; --radius:14px;
  --mono:"BSOS Mono","BSOS Sans TC",ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  color-scheme:light;
}
*{box-sizing:border-box;}
html{scroll-behavior:smooth; scroll-padding-top:80px;}
body{margin:0; background:var(--bg); color:var(--ink); font-size:16px; line-height:1.75;
  font-family:'gotham',"BSOS Sans TC",'Noto Sans TC',system-ui,-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;
  -webkit-font-smoothing:antialiased;}
h1,h2,h3{line-height:1.35; margin:0;}
p{margin:0 0 12px;}
a{color:var(--brand); text-decoration:none;}
strong{font-weight:500; color:var(--ink);}
code{font-family:var(--mono); font-size:.9em;}
:not(pre)>code{background:var(--surface-2); border:1px solid var(--line); border-radius:6px; padding:0 5px; overflow-wrap:anywhere;}

.app-header{position:sticky; top:0; z-index:50; background:rgba(255,255,255,.92);
  backdrop-filter:saturate(180%) blur(10px); border-bottom:1px solid var(--line);}
.app-header .bar{display:flex; align-items:center; justify-content:space-between; gap:16px; height:62px; padding:0 28px;}
.logo svg{height:26px; width:auto; display:block;}
.app-header .doc{font-size:14px; font-weight:500; color:var(--muted); white-space:nowrap; overflow:hidden; text-overflow:ellipsis;}

.layout{max-width:1220px; margin:0 auto; padding:0 24px; display:grid; grid-template-columns:256px minmax(0,1fr); gap:34px;}
.toc{position:sticky; top:62px; align-self:start; max-height:calc(100vh - 62px); overflow:auto; padding:40px 0 24px; font-size:14.5px;}
.toc .kicker{margin-bottom:8px;}
.toc ol{list-style:none; margin:0; padding:0;}
.toc li a{display:block; padding:5px 10px; border-radius:8px; color:var(--ink-2); transition:.15s;}
.toc li a:hover{background:var(--surface-2); color:var(--brand);}
.toc li a.on{color:var(--brand); background:var(--brand-soft);}
.toc li li a{padding-left:28px; font-size:14px; color:var(--muted);}
.toc .n{display:inline-block; width:18px; color:var(--muted); font-variant-numeric:tabular-nums;}
main{min-width:0; max-width:760px; padding-bottom:72px;}

.hero{padding:44px 0 8px;}
.kicker{font-size:14.5px; font-weight:500; letter-spacing:.4px; color:var(--muted);}
.hero h1{font-size:clamp(28px,3.8vw,42.5px); font-weight:700; letter-spacing:-.5px; color:var(--brand); margin-top:8px;}
.hero .lede{margin-top:12px; font-size:17.5px; line-height:1.8; color:var(--ink-2); max-width:64ch;}
.four{display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin-top:22px;}
.fq{background:var(--surface-2); border-radius:var(--radius-sm); padding:11px 14px; min-width:0;}
.fq .k{font-size:13.5px; font-weight:500; color:var(--muted); margin:0 0 3px;}
.fq .v{font-size:15px; line-height:1.55; margin:0; overflow-wrap:anywhere;}

.chapter{padding-top:54px;}
.chapter > header{display:flex; gap:16px; align-items:flex-start; margin-bottom:18px;}
.num{flex:none; width:44px; height:44px; border-radius:var(--radius); background:var(--brand-soft); color:var(--brand);
  display:flex; align-items:center; justify-content:center; font-size:18px;}
.chapter h2{font-size:clamp(24px,2.6vw,30px); font-weight:700; letter-spacing:-.3px; color:var(--brand); padding-top:2px;}
.chapter .caption{margin:4px 0 0; color:var(--ink-2); font-size:16px;}
.chapter h3{font-size:18px; font-weight:500; margin:34px 0 10px;}
main p, main li{max-width:64ch;}
main ul, main ol{padding-left:22px; margin:0 0 12px;}
main li{margin:4px 0;}

.table-wrap{overflow-x:auto; margin:6px 0 18px;}
table{width:100%; border-collapse:collapse; font-size:15px;}
th{text-align:left; font-size:13.5px; font-weight:500; color:var(--muted); padding:9px 14px 9px 0; border-bottom:1px solid var(--line-2); white-space:nowrap;}
td{padding:10px 14px 10px 0; border-bottom:1px solid var(--line); color:var(--ink-2); vertical-align:top;}
td.k{color:var(--ink); font-weight:500;}
table.tight-first td.k{white-space:nowrap;}

.code{position:relative; margin:6px 0 16px;}
pre{margin:0; background:var(--surface-2); border:1px solid var(--line); border-radius:var(--radius-sm);
  padding:14px 16px; font-size:14px; line-height:1.65; white-space:pre-wrap; overflow-wrap:anywhere;}
pre code{font-size:1em;}
.code pre{padding-right:76px;}
.copy{position:absolute; top:8px; right:8px; font:inherit; font-size:13px; font-weight:500; color:var(--brand);
  background:#fff; border:1px solid var(--line-2); border-radius:8px; padding:2px 10px; cursor:pointer; transition:.15s;}
.copy:hover{border-color:var(--brand);}
.copy:focus-visible,.tab:focus-visible{outline:2px solid var(--brand); outline-offset:2px;}

.tabs{margin:6px 0 16px;}
.tablist{display:flex; gap:4px; border-bottom:1px solid var(--line-2); margin-bottom:10px; overflow-x:auto;}
.tab{font:inherit; font-size:14.5px; font-weight:500; color:var(--muted); background:none; border:0; border-bottom:2px solid transparent;
  padding:8px 12px; margin-bottom:-1px; cursor:pointer; white-space:nowrap; transition:.15s;}
.tab:hover{color:var(--ink);}
.tab[aria-selected="true"]{color:var(--brand); border-bottom-color:var(--brand);}
.tabs .label{display:none;}
.tabs .code{margin:0;}

.note{background:var(--soft); border-radius:12px; padding:14px 16px; margin:16px 0; font-size:15px; color:var(--ink-2);}
.note p{margin:0 0 6px;} .note p:last-child{margin:0;}

footer{background:var(--surface-2); border-top:1px solid var(--line); padding:32px 28px; font-size:14px; color:var(--muted);}
footer .logo svg{height:20px; margin-bottom:10px;}

@media (max-width:1024px){
  .layout{grid-template-columns:minmax(0,1fr);}
  .toc{display:none;}
  main{margin:0 auto;}
}
@media (max-width:600px){
  .layout{padding:0 16px;}
  .app-header .bar{padding:0 16px;}
  .four{grid-template-columns:minmax(0,1fr);}
  .chapter > header{gap:12px;}
  .num{width:36px; height:36px; font-size:16px;}
}
@media (prefers-reduced-motion:reduce){ html{scroll-behavior:auto;} *{transition:none !important;} }
@media print{
  .app-header{position:static;} .toc,.copy,.tablist{display:none !important;}
  .layout{display:block; max-width:none; padding:0;} main{max-width:none;}
  .tabs [role=tabpanel]{display:block !important; margin-bottom:12px;}
  .tabs .label{display:block; font-weight:500; margin-bottom:4px;}
  .code pre{padding-right:16px;}
  pre,.table-wrap,.note{break-inside:avoid;}
  .chapter{padding-top:28px;}
  footer{background:none;}
}
"""

JS = """
document.querySelectorAll('.copy').forEach((b) => b.addEventListener('click', () => {
  const text = b.parentElement.querySelector('pre').innerText;
  const done = () => { b.textContent = b.dataset.done; setTimeout(() => { b.textContent = b.dataset.label; }, 1500); };
  if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done); return; }
  // file:// and plain-http pages have no clipboard API; fall back to select + execCommand
  const ta = document.createElement('textarea'); ta.value = text; document.body.appendChild(ta); ta.select();
  document.execCommand('copy'); ta.remove(); done();
}));
document.querySelectorAll('.tabs').forEach((box) => {
  const tabs = [...box.querySelectorAll('.tab')], panels = [...box.querySelectorAll('[role=tabpanel]')];
  const pick = (i) => tabs.forEach((t, j) => { t.setAttribute('aria-selected', i === j); t.tabIndex = i === j ? 0 : -1; panels[j].hidden = i !== j; });
  tabs.forEach((t, i) => {
    t.addEventListener('click', () => pick(i));
    t.addEventListener('keydown', (e) => {
      const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
      if (d) { const n = (i + d + tabs.length) % tabs.length; pick(n); tabs[n].focus(); }
    });
  });
  pick(0);
});
const links = [...document.querySelectorAll('.toc a')];
const spy = new IntersectionObserver((es) => es.forEach((e) => {
  if (e.isIntersecting) links.forEach((a) => a.classList.toggle('on', a.hash === '#' + e.target.id));
}), { rootMargin: '-70px 0px -70% 0px' });
document.querySelectorAll('main h2[id], main h3[id]').forEach((h) => spy.observe(h));
"""


def front_matter(text):
    m = re.match(r"---\n(.*?)\n---\n", text, re.S)
    meta, key = {}, None
    for line in m.group(1).splitlines():
        if line.startswith("  ") and key:
            meta[key] += line.strip() + "\n"
        else:
            key, _, val = line.partition(":")
            key, val = key.strip(), val.strip()
            meta[key] = "" if val == "|" else val
    return meta, text[m.end():]


def to_html(body):
    # :::note / :::tabs -> md_in_html blocks
    body = re.sub(r":::(note|tabs)\n(.*?)\n:::", r'<div class="\1" markdown="1">\n\2\n</div>', body, flags=re.S)
    return markdown.markdown(body, extensions=["tables", "fenced_code", "md_in_html"])


def decorate(soup, meta):
    """Section numbers and ids -> section heads (number, h2, description line) -> tables -> code copy buttons -> tabs -> bold list labels."""
    h2n = h3n = 0
    toc = []
    for h in soup.find_all(["h2", "h3"]):
        if h.name == "h2":
            h2n, h3n = h2n + 1, 0
            h["id"] = f"sec-{h2n}"
            toc.append((h2n, h.get_text().strip(), h["id"], []))
        else:
            h3n += 1
            h["id"] = f"sec-{h2n}-{h3n}"
            toc[-1][3].append((h.get_text().strip(), h["id"]))
    for i, h2 in enumerate(list(soup.find_all("h2")), 1):
        section = soup.new_tag("section", attrs={"class": "chapter"})
        members, node = [h2], h2.next_sibling
        while node is not None and getattr(node, "name", None) != "h2":
            members.append(node)
            node = node.next_sibling
        h2.insert_before(section)
        for m in members:
            section.append(m.extract())
        head = soup.new_tag("header")
        h2.insert_before(head)
        num = soup.new_tag("div", attrs={"class": "num", "aria-hidden": "true"})
        num.string = str(i)
        titles = soup.new_tag("div")
        head.append(num)
        head.append(titles)
        titles.append(h2.extract())
        nxt = head.find_next_sibling()
        if nxt is not None and nxt.name == "blockquote":
            cap = soup.new_tag("p", attrs={"class": "caption"})
            cap.string = nxt.get_text().strip()
            nxt.decompose()
            titles.append(cap)
    for table in soup.find_all("table"):
        # Blank first column = same as the row above (merged cell)
        last = None
        for tr in table.find_all("tr"):
            first = tr.find(["td", "th"])
            if first.name == "td" and not first.get_text(strip=True) and last is not None:
                last["rowspan"] = str(int(last.get("rowspan", 1)) + 1)
                first.decompose()
            elif first.name == "td":
                first["class"] = ["k"]
                last = first
        if all(len(td.get_text()) <= 10 for td in table.select("tbody tr > td:first-child")):
            table["class"] = ["tight-first"]
        table.wrap(soup.new_tag("div", attrs={"class": "table-wrap"}))
    for pre in soup.find_all("pre"):
        box = soup.new_tag("div", attrs={"class": "code"})
        pre.wrap(box)
        btn = soup.new_tag("button", attrs={"class": "copy", "type": "button", "data-label": meta["copy"], "data-done": meta["copied"]})
        btn.string = meta["copy"]
        box.append(btn)
    for n, box in enumerate(soup.select("div.tabs")):
        # A "Label:" paragraph plus the code right after it = one tab
        pairs = [(p, p.find_next_sibling()) for p in box.find_all("p", recursive=False)]
        tablist = soup.new_tag("div", attrs={"class": "tablist", "role": "tablist"})
        for i, (p, code) in enumerate(pairs):
            label = p.get_text().strip().rstrip("：:")
            tid, pid = f"tab-{n}-{i}", f"panel-{n}-{i}"
            tab = soup.new_tag("button", attrs={"class": "tab", "type": "button", "role": "tab", "id": tid, "aria-controls": pid})
            tab.string = label
            tablist.append(tab)
            panel = soup.new_tag("div", attrs={"role": "tabpanel", "id": pid, "aria-labelledby": tid})
            code.wrap(panel)
            p["class"] = ["label"]
            panel.insert(0, p.extract())
        box.insert(0, tablist)
    for li in soup.find_all("li"):
        head = li.contents[0] if li.contents else None
        if isinstance(head, str) and 0 < head.find("：") <= 26 or isinstance(head, str) and re.match(r"^[^:]{1,26}: ", head):
            text = str(head)
            i = text.find("：") if "：" in text else text.find(":")
            label = soup.new_tag("strong")
            label.string = text[:i]
            head.replace_with(label)
            label.insert_after(text[i:])
    return toc


def toc_html(meta, toc):
    items = []
    for n, title, hid, subs in toc:
        sub = "".join(f'<li><a href="#{sid}">{t}</a></li>' for t, sid in subs)
        items.append(f'<li><a href="#{hid}"><span class="n">{n}</span>{title}</a>' + (f"<ol>{sub}</ol>" if sub else "") + "</li>")
    return f'<nav class="toc" aria-label="{meta["toc"]}"><div class="kicker">{meta["toc"]}</div><ol>{"".join(items)}</ol></nav>'


def hero(meta):
    lede = "".join(f'<p class="lede">{p}</p>' for p in meta["lede"].strip().split("\n\n"))
    facts = "".join(f'<div class="fq"><p class="k">{k}</p><p class="v">{v}</p></div>'
                    for k, v in (line.split(" | ", 1) for line in meta["facts"].strip().splitlines()))
    return (f'<section class="hero"><div class="kicker">{meta["eyebrow"]}</div><h1>{meta["title"]}</h1>'
            f'{lede}<div class="four">{facts}</div></section>')


def subset_face(path, chars, family, weight):
    """Subset a woff2 down to only chars and return an @font-face; return None if nothing matches."""
    font = TTFont(path, recalcTimestamp=False)  # do not overwrite head.modified, so regenerating does not replace the whole file
    have = {c for c in chars if ord(c) in font.getBestCmap()}
    if not have:
        return None
    opts = Options()
    opts.flavor = "woff2"
    opts.layout_features = ["*"]
    opts.name_IDs = [0, 13, 14]  # keep only copyright, license text and license URL (OFL clause 2); family names are still dropped
    opts.notdef_outline = False
    sub = Subsetter(opts)
    sub.populate(unicodes=[ord(c) for c in have])
    sub.subset(font)
    buf = io.BytesIO()
    font.flavor = "woff2"
    font.save(buf)
    rng = ",".join(f"U+{ord(c):X}" for c in sorted(have))
    return (f'@font-face{{font-family:"{family}";font-style:normal;font-weight:{weight};'
            f'src:url(data:font/woff2;base64,{base64.b64encode(buf.getvalue()).decode()}) format("woff2");'
            f'unicode-range:{rng};}}')


def font_css(by_weight, mono):
    # A subset counts as a modified version under OFL and may not keep a reserved font name (IBM Plex reserves "Plex"), so both families are renamed; license in NOTICE, third_party/fonts/
    faces = [subset_face(FONTS / "ibm-plex-mono/files/ibm-plex-mono-latin-400-normal.woff2", mono, "BSOS Mono", 400)]
    # Noto Sans TC is split into hundreds of chunks by unicode-range (including latin): pick out the chunks actually needed.
    # Every chunk carries ASCII; each character goes only into the first chunk containing it, otherwise it would be duplicated hundreds of times
    files = sorted((FONTS / "noto-sans-tc/files").glob("noto-sans-tc-*-400-normal.woff2"),
                   key=lambda f: (not re.search(r"-latin-\d", f.name), f.name))
    for w, chars in by_weight.items():
        left = set(chars)
        for f in files:
            f = f.with_name(f.name.replace("-400-", f"-{w}-"))
            face = subset_face(f, left, "BSOS Sans TC", w)
            if face:
                faces.append(face)
                left -= {chr(int(u[2:], 16)) for u in face.rsplit("unicode-range:", 1)[1].rstrip(";}").split(",")}
    return "\n".join(f for f in faces if f)


def build(md_path, out_path, logo):
    meta, body = front_matter(md_path.read_text())
    soup = BeautifulSoup(to_html(body), "html.parser")
    toc = decorate(soup, meta)
    page = f"""<header class="app-header"><div class="bar"><span class="logo" aria-label="BSOS">{logo}</span><span class="doc">{meta['footer']}</span></div></header>
<div class="layout">
{toc_html(meta, toc)}
<main>
{hero(meta)}
{soup}
</main>
</div>
<footer><span class="logo">{logo}</span><div>{meta['footer']}</div></footer>"""
    ps = BeautifulSoup(page, "html.parser")
    extra = EXTRA_ZH if meta["lang"].startswith("zh") else ""
    by_weight = {400: ps.get_text() + meta["title"] + extra + ASCII + "·→①②③④–"}
    for w, sels in WEIGHTS.items():
        by_weight[w] = "".join(e.get_text() for s in sels for e in ps.select(s)) + ASCII
    mono = ASCII + "".join(e.get_text() for e in ps.select("code"))
    html = f"""<!DOCTYPE html>
<html lang="{meta['lang']}">
<!-- {FONT_NOTICE} -->
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{meta['title']} · {{{{NODE_ID}}}}</title>
<style>
{font_css(by_weight, mono)}
{CSS}
</style>
</head>
<body>
{page}
<script>{JS}</script>
</body>
</html>
"""
    out_path.write_text(html)
    print(f"{out_path.relative_to(ROOT)}  {len(html.encode()) // 1024} KB")


def main():
    logo = (HERE / "assets/bsos-logo.svg").read_text().strip()
    # Sizing is left to CSS; an inline style would override the .logo svg height
    logo = re.sub(r"\s(style|id|data-name)=(['\"]).*?\2", "", logo)
    OUT.mkdir(parents=True, exist_ok=True)
    for lang in ("zh", "en"):
        build(HERE / f"{lang}.md", OUT / f"runbook.{lang}.html", logo)


if __name__ == "__main__":
    main()

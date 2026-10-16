#!/usr/bin/env python3
"""Patch the downloaded management panel HTML for the XRouter brand.

The control panel (management.html) is a prebuilt bundle downloaded from the
upstream panel repository at runtime. This script applies the XRouter brand to
a local copy so the server serves a branded panel without forking the panel
repository yet. It is re-runnable: after the panel auto-update overwrites the
file, run it again (also keep management.disable-auto-update-panel enabled to
avoid being overwritten in the first place).

Usage:
  python3 scripts/patch-management-panel.py [path/to/management.html]

Default target: static/management.html (relative to the repository root).
"""

from __future__ import annotations

import base64
import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
ICON_SVG = REPO_ROOT / "assets" / "brand" / "xrouter-icon.svg"


def b64_icon_data_uri() -> str:
    encoded = base64.b64encode(ICON_SVG.read_bytes()).decode("ascii")
    return f"data:image/svg+xml;base64,{encoded}"


# Simple textual replacements: (label, old, new).
# Order matters: the full main-repo URL must be rewritten before the compact
# brand replacement turns "CLIProxyAPI" into "XRouter" inside that URL.
REPLACEMENTS: list[tuple[str, str, str]] = [
    ("main repo link", "https://github.com/router-for-me/CLIProxyAPI", "https://github.com/xnuyoah/XRouter"),
    ("main repo link (after brand text)", "https://github.com/router-for-me/XRouter", "https://github.com/xnuyoah/XRouter"),
    ("brand text", "CLI Proxy API", "XRouter"),
    ("brand text (compact)", "CLIProxyAPI", "XRouter"),
    ("title abbreviation", "abbr:`CPAMC`", "abbr:`XRouter`"),
    ("official namespace prefix", "UM=`https://github.com/router-for-me/`", "UM=`https://github.com/xnuyoah/xrouter`"),
    ("kimi affiliate link (cn)", "https://platform.kimi.com/?aff=cliproxyapi", "https://platform.kimi.com/"),
    ("kimi affiliate link (global)", "https://platform.kimi.ai/?aff=cliproxyapi", "https://platform.kimi.ai/"),
    ("webui storage key", "cli-proxy-api-webui::secure-storage", "xrouter-webui::secure-storage"),
    ("auth-dir placeholder", "placeholder:`~/.cli-proxy-api`", "placeholder:`~/.xrouter`"),
    ("logo alt text (compact)", "alt:`CPAMC logo`", "alt:`XRouter logo`"),
    ("logo alt text", "alt:`CPAMC`", "alt:`XRouter`"),
]

# Regex replacements: (label, pattern, replacement).
REGEX_REPLACEMENTS: list[tuple[str, str, str]] = [
    # Plugin trust-gate copy names the official organization; keep the upstream
    # panel repository references (owner/repo paths) untouched.
    ("official organization name", r"router-for-me(?![/-])", "xnuyoah"),
]

# Sidebar entry removals: (label, exact snippet removed).
NAV_REMOVALS: list[tuple[str, str]] = [
    (
        "nav item definition (quick start)",
        "pe={path:`/quick-start`,label:fe?bS:void 0,labelKey:fe?void 0:`nav.quick_start`,metaKey:`nav_meta.quick_start`,icon:K9.quickStart},",
    ),
    (
        "nav placement (quick start, operate group)",
        ",...fe?[]:[pe]",
    ),
    (
        "nav items (auth files + oauth)",
        ",{path:`/auth-files`,labelKey:`nav.auth_files`,metaKey:`nav_meta.auth_files`,badge:v??void 0,badgeLabel:typeof v==`number`?e(`sidebar.auth_files_count`,{count:v}):void 0,icon:K9.authFiles},{path:`/oauth`,labelKey:`nav.oauth`,metaKey:`nav_meta.oauth`,icon:K9.oauth},...fe?[pe]:[]",
    ),
    (
        "nav item (plugin store)",
        ",{path:`/plugin-store`,labelKey:`nav.plugin_store`,metaKey:`nav_meta.plugin_store`,icon:K9.pluginStore}",
    ),
]

# Route redirects: (label, old element, new element) — removed pages now redirect to "/".
REDIRECT_PAGES = [
    ("/quick-start", "{path:`/quick-start`,element:(0,H.jsx)(fD,{fixedBrand:`apikeyFun`})}"),
    ("/auth-files", "{path:`/auth-files`,element:(0,H.jsx)(Rj,{})}"),
    ("/auth-files/oauth-excluded", "{path:`/auth-files/oauth-excluded`,element:(0,H.jsx)(Tne,{})}"),
    ("/auth-files/oauth-model-alias", "{path:`/auth-files/oauth-model-alias`,element:(0,H.jsx)(Qj,{})}"),
    ("/oauth", "{path:`/oauth`,element:(0,H.jsx)(Hne,{})}"),
    ("/plugin-store", "{path:`/plugin-store`,element:(0,H.jsx)(IF,{})}"),
]


def main() -> int:
    if not ICON_SVG.exists():
        print(f"icon not found: {ICON_SVG}", file=sys.stderr)
        return 1
    target = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else REPO_ROOT / "static" / "management.html"
    if not target.exists():
        print(f"panel not found: {target}", file=sys.stderr)
        return 1

    text = target.read_text(encoding="utf-8")
    report: list[str] = []

    for label, old, new in REPLACEMENTS:
        count = text.count(old)
        if count:
            text = text.replace(old, new)
        report.append(f"  {label}: {count}")

    for label, pattern, new in REGEX_REPLACEMENTS:
        text, count = re.subn(pattern, new, text)
        report.append(f"  {label}: {count}")

    favicon = re.compile(r'(<link rel="icon" type="image/svg\+xml" href=")data:image/svg\+xml,[^"]*(")')
    text, favicon_count = favicon.subn(rf"\g<1>{b64_icon_data_uri()}\g<2>", text)
    report.append(f"  favicon: {favicon_count}")

    # The login/sidebar logo is an inline JPEG assigned to a bundle-local variable.
    text, logo_count = re.subn(r"(x=`)data:image/jpeg;base64,[A-Za-z0-9+/=]+(`)", rf"\g<1>{b64_icon_data_uri()}\g<2>", text)
    report.append(f"  logo: {logo_count}")

    for label, snippet in NAV_REMOVALS:
        count = text.count(snippet)
        if count:
            text = text.replace(snippet, "")
        report.append(f"  {label}: {count}")

    for label, old in REDIRECT_PAGES:
        count = text.count(old)
        if count:
            new = f"{{path:`{label}`,element:(0,H.jsx)(ti,{{to:`/`,replace:!0}})}}"
            text = text.replace(old, new)
        report.append(f"  redirect {label}: {count}")

    target.write_text(text, encoding="utf-8")
    print(f"patched {target}")
    print("\n".join(report))
    leftovers = len(re.findall(r"CLI Proxy API|CLIProxyAPI|aff=cliproxyapi", text))
    print(f"leftover brand strings: {leftovers}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

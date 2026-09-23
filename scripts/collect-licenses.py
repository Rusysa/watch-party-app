#!/usr/bin/env python3
"""Collect license notices from downloaded Go modules for RPM distribution.

Run from watchparty/: go list -deps -json . | python3 ../scripts/collect-licenses.py
"""
import json
import pathlib
import re
import shutil
import sys

root = pathlib.Path(__file__).resolve().parents[1]
dest = root / 'watchparty/build/bin/third-party'
if dest.exists():
    shutil.rmtree(dest)
dest.mkdir(parents=True, exist_ok=True)
raw = sys.stdin.read()
decoder = json.JSONDecoder()
index = ['# Go module license notices', '', 'Generated from the selected Go module versions.', '']
missing = []
modules = {}
while raw.strip():
    raw = raw.lstrip()
    package, consumed = decoder.raw_decode(raw)
    raw = raw[consumed:]
    module = package.get('Module')
    if module and not module.get('Main'):
        modules[module['Path'] + '@' + module.get('Version', '')] = module
for module in sorted(modules.values(), key=lambda item: item['Path']):
    name = module['Path']
    version = module.get('Version', '')
    folder = pathlib.Path(module.get('Dir', ''))
    # Paths come from `go list`, not user input. Keep output names flat.
    safe = re.sub(r'[^a-zA-Z0-9_.-]', '_', name + '@' + version)
    notices = []
    if folder.is_dir():
        for file in sorted(folder.iterdir()):
            if file.is_file() and re.match(r'^(LICENSE|LICENCE|COPYING|NOTICE)(\.|$)', file.name, re.I):
                target = safe + '-' + file.name
                (dest / target).write_bytes(file.read_bytes())
                notices.append(target)
    if not notices:
        missing.append(name + '@' + version)
    index.append(f'- {name} {version}: ' + (', '.join(notices) if notices else 'verify upstream notice'))
(dest / 'INDEX.md').write_text('\n'.join(index) + '\n', encoding='utf-8')
if not modules:
    sys.exit('No Go dependencies were found; check go list output')
if missing:
    print('Modules without a root-level license notice (review upstream):', file=sys.stderr)
    print('\n'.join(missing), file=sys.stderr)
    sys.exit(1)

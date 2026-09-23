#!/usr/bin/env bash
set -euo pipefail

version="${1:?Uso: scripts/build-rpm.sh VERSION (sin prefijo v)}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo 'La versión debe tener formato X.Y.Z' >&2
  exit 1
fi
root="$(cd "$(dirname "$0")/.." && pwd)"
output="$root/watchparty/build/bin/watchparty"
test -x "$output" || { echo 'Compila watchparty primero' >&2; exit 1; }
topdir="$(mktemp -d)"
trap 'rm -rf "$topdir"' EXIT
mkdir -p "$topdir/SOURCES" "$topdir/RPMS" "$topdir/BUILD" "$topdir/BUILDROOT"
cp "$output" "$topdir/SOURCES/watchparty"
cp "$root/packaging/rpm/watchparty.desktop" "$topdir/SOURCES/"
cp "$root/watchparty/build/appicon.png" "$topdir/SOURCES/"
cp "$root/docs/credits.md" "$topdir/SOURCES/"
cp "$root/README.md" "$topdir/SOURCES/"
test -s "$root/LICENSE" && test -s "$root/packaging/rpm/license.spdx" && \
  test -s "$root/packaging/rpm/rpm-license.spdx" || { echo 'Falta la licencia del proyecto o su expresión SPDX' >&2; exit 1; }
test -s "$root/watchparty/build/bin/third-party/INDEX.md" || {
  echo 'Recopila primero los avisos de dependencias Go (consulta docs/packaging.md)' >&2; exit 1;
}
license="$(cat "$root/packaging/rpm/rpm-license.spdx")"
if ! grep -Eq '^[A-Za-z0-9.+() -]+$' <<< "$license"; then echo 'Expresión SPDX inválida' >&2; exit 1; fi
cp "$root/LICENSE" "$topdir/SOURCES/"
cp -r "$root/watchparty/build/bin/third-party" "$topdir/SOURCES/"
rpmbuild -bb "$root/packaging/rpm/watchparty.spec" \
  --define "_topdir $topdir" --define "app_version $version" --define "app_license $license"
find "$topdir/RPMS" -name '*.rpm' -exec cp {} "$root/watchparty/build/bin/" \;

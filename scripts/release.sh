#!/usr/bin/env bash
# Builds Lerryn release artifacts on a maintainer's machine. GitHub Actions
# is off, so this is the release pipeline (docs/release.md).
#
#   scripts/release.sh image      multi-arch image with docker buildx
#   scripts/release.sh binaries   lerryn binaries for darwin/linux, amd64/arm64, with SHA256SUMS
#
# Nothing is pushed unless RELEASE_PUSH=1. With RELEASE_PUSH=1 or RELEASE=1
# the version must be a release tag on a clean checkout of HEAD.
#
# Environment:
#   VERSION          release version, e.g. v0.9.0 or v0.9.0-rc.1 (default: git describe)
#   IMAGE            image repository (default: ghcr.io/niltavi/lerryn)
#   PLATFORMS        image platforms (default: linux/amd64,linux/arm64)
#   RELEASE          1 enforces the release checks without pushing
#   RELEASE_PUSH     1 pushes the image (implies RELEASE=1); anything else builds only
#   RELEASE_LOAD     1 loads a single-platform image into the local daemon
#   BUILDX_BUILDER   buildx builder to use (default: the current one)
#   DIST             output directory for binaries (default: dist)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
# The commit time, not the wall clock, so that rebuilding a tag stamps the
# same date.
DATE="${DATE:-$(TZ=UTC git show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)}"
IMAGE="${IMAGE:-ghcr.io/niltavi/lerryn}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
RELEASE_PUSH="${RELEASE_PUSH:-0}"
RELEASE_LOAD="${RELEASE_LOAD:-0}"
STRICT=0
[[ "$RELEASE_PUSH" == 1 || "${RELEASE:-0}" == 1 ]] && STRICT=1
DIST="${DIST:-dist}"
BINARY_PLATFORMS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64"

RELEASE_TAG='^v([0-9]+)\.([0-9]+)\.([0-9]+)(-rc\.[0-9]+)?$'

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'release: %s\n' "$*" >&2; exit 1; }

spec_version() {
  awk '/^info:/{i=1; next} i && /^  version:/{print $2; exit}' api/openapi/openapi.yaml
}

# Checks that apply to every release build. A version that is not a release
# tag (a dev build) only gets a warning unless something is being published.
check_version() {
  local strict="$1"
  if [[ ! "$VERSION" =~ $RELEASE_TAG ]]; then
    [[ "$strict" == 1 ]] && die "VERSION=$VERSION is not a release tag (vX.Y.Z or vX.Y.Z-rc.N)"
    log "VERSION=$VERSION is not a release tag; building a development artifact"
    return
  fi
  local core="${VERSION#v}"
  core="${core%%-*}"
  local spec
  spec="$(spec_version)"
  [[ "$core" == "$spec" ]] || die "VERSION=$VERSION does not match info.version $spec in api/openapi/openapi.yaml"
  if [[ "$strict" == 1 ]]; then
    [[ -z "$(git status --porcelain)" ]] || die "the working tree has uncommitted changes"
    [[ "$(git rev-parse HEAD)" == "$(git rev-parse "$VERSION^{commit}" 2>/dev/null)" ]] \
      || die "HEAD is not the commit tagged $VERSION"
    node scripts/notices.mjs --check || die "THIRD_PARTY_NOTICES is out of date"
  fi
}

image_tags() {
  local tag="${VERSION#v}"
  printf '%s\n' "$IMAGE:$tag"
  # A final release also moves the minor tag; candidates get only their own.
  if [[ "$VERSION" =~ $RELEASE_TAG && -z "${BASH_REMATCH[4]}" ]]; then
    printf '%s\n' "$IMAGE:${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"
  fi
}

cmd_image() {
  check_version "$STRICT"
  local args=(buildx build --platform "$PLATFORMS"
    --build-arg "VERSION=$VERSION" --build-arg "COMMIT=$COMMIT" --build-arg "DATE=$DATE")
  [[ -n "${BUILDX_BUILDER:-}" ]] && args+=(--builder "$BUILDX_BUILDER")
  local tag
  while IFS= read -r tag; do args+=(-t "$tag"); done < <(image_tags)

  if [[ "$RELEASE_PUSH" == 1 ]]; then
    [[ "$RELEASE_LOAD" == 1 ]] && die "RELEASE_PUSH and RELEASE_LOAD are exclusive"
    args+=(--push)
    log "building and pushing $(image_tags | tr '\n' ' ')for $PLATFORMS"
  elif [[ "$RELEASE_LOAD" == 1 ]]; then
    [[ "$PLATFORMS" != *,* ]] || die "RELEASE_LOAD needs a single platform, e.g. PLATFORMS=linux/arm64"
    args+=(--load)
    log "building $(image_tags | tr '\n' ' ')for $PLATFORMS and loading it into the local daemon"
  else
    # Neither pushed nor loaded: every platform is built to prove it builds.
    # The result stays in the builder, or in the local image store when the
    # driver is backed by containerd.
    log "building $(image_tags | tr '\n' ' ')for $PLATFORMS without pushing (RELEASE_PUSH=1 pushes)"
  fi
  docker "${args[@]}" .
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

cmd_binaries() {
  check_version "$STRICT"
  local out="$DIST/$VERSION"
  rm -rf "$out"
  mkdir -p "$out"

  # Every binary embeds the same console; build it once.
  log "building the web console"
  make --no-print-directory web >&2

  local ldflags="-s -w -X github.com/niltavi/lerryn/server/internal/buildinfo.Version=$VERSION"
  ldflags+=" -X github.com/niltavi/lerryn/server/internal/buildinfo.Commit=$COMMIT"
  ldflags+=" -X github.com/niltavi/lerryn/server/internal/buildinfo.Date=$DATE"

  local platform goos goarch name
  for platform in $BINARY_PLATFORMS; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    name="lerryn_${VERSION#v}_${goos}_${goarch}"
    log "building $name"
    mkdir -p "$out/$name"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" \
      -o "$out/$name/lerryn" ./server/cmd/lerryn
    cp LICENSE THIRD_PARTY_NOTICES "$out/$name/"
    COPYFILE_DISABLE=1 tar -C "$out" -czf "$out/$name.tar.gz" "$name"
    rm -rf "${out:?}/$name"
  done
  (cd "$out" && sha256 ./*.tar.gz | sed 's# \./# #' > SHA256SUMS)
  log "wrote $out"
  cat "$out/SHA256SUMS" >&2
}

case "${1:-}" in
  image) cmd_image ;;
  binaries) cmd_binaries ;;
  *) die "usage: scripts/release.sh image|binaries" ;;
esac

#!/bin/sh
# herdr [[build]] step. Download a prebuilt binary only when its digest is in
# the reviewed release-checksums.txt allowlist. A missing/mismatched entry
# always falls back to a local source build; a checksum sidecar from GitHub is
# never trusted as an authority.
set -eu

# Resolve the checkout without invoking a PATH-provided helper. The build
# script is itself inside the checkout, so using an untrusted `dirname` before
# sanitising PATH would let a same-named executable run first.
script_path=$0
case "$script_path" in
  */*) script_dir=${script_path%/*}; [ -n "$script_dir" ] || script_dir=/ ;;
  *) script_dir=. ;;
esac
root=$(CDPATH= cd "$script_dir/.." && pwd -P)

# Never let a relative PATH entry (especially `.`) resolve helper commands from
# the checkout itself. A hostile tree could otherwise shadow git/curl/go before
# the integrity checks run.
old_ifs=${IFS-}
set -f
IFS=:
safe_path=
for path_entry in ${PATH-}; do
  case "$path_entry" in
    ""|.) ;;
    /*)
      # Reject both lexical checkout paths and symlinked PATH directories that
      # resolve into the checkout. This covers `.venv/bin` and `node_modules`
      # entries injected by a project-level environment.
      inside=0
      case "$path_entry" in
        "$root"|"$root"/*) inside=1 ;;
        *)
          if [ -d "$path_entry" ]; then
            resolved=$(CDPATH= cd "$path_entry" 2>/dev/null && pwd -P) || resolved=
            case "$resolved" in
              "$root"|"$root"/*) inside=1 ;;
            esac
          fi
          ;;
      esac
      [ "$inside" -eq 1 ] || safe_path=${safe_path:+$safe_path:}$path_entry
      ;;
  esac
done
IFS=$old_ifs
set +f
[ -n "$safe_path" ] || safe_path=/usr/bin:/bin
PATH=$safe_path
export PATH

# Do not let inherited Git variables redirect this installer's repository or
# replace its helpers/transports. The script only needs Git to identify a
# canonical checkout.
unset GIT_CONFIG GIT_CONFIG_COUNT GIT_CONFIG_SYSTEM GIT_CONFIG_GLOBAL \
  GIT_EXTERNAL_DIFF GIT_DIFF_OPTS GIT_PAGER GIT_SSH GIT_SSH_COMMAND \
  GIT_PROXY_COMMAND GIT_ASKPASS GIT_EDITOR GIT_SEQUENCE_EDITOR GIT_DIR \
  GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
  GIT_COMMON_DIR GIT_CEILING_DIRECTORIES GIT_SSL_NO_VERIFY 2>/dev/null || :
unset BASH_ENV ENV IFS SHELLOPTS BASHOPTS CDPATH \
  LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT LD_DEBUG LD_DEBUG_OUTPUT \
  DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH DYLD_FRAMEWORK_PATH DYLD_FALLBACK_LIBRARY_PATH \
  DYLD_FALLBACK_FRAMEWORK_PATH DYLD_ROOT_PATH DYLD_SHARED_REGION 2>/dev/null || :
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
  GIT_TERMINAL_PROMPT=0 GIT_PAGER=cat GIT_SSH_COMMAND=ssh
export GH_HOST=github.com GH_PAGER=cat PAGER=cat LESS=-FRX NO_COLOR=1 CLICOLOR=0

cd "$root"

bin=herdr-gh-checks
repo=itisbryan/herdr-gh-checks
version=$(sed -n 's/^version = "\(.*\)"/\1/p' herdr-plugin.toml | head -1)
tmp=
build_tmp=

cleanup() {
  [ -z "${build_tmp:-}" ] || rm -f -- "$build_tmp"
  [ -z "${tmp:-}" ] || rm -rf -- "$tmp"
}
trap cleanup EXIT

source_repo_matches() {
  # A checkout with Git metadata must identify the same canonical repository as
  # the release URL. Forks and modified checkouts build locally instead of
  # silently running a binary from another owner.
  if [ ! -e .git ]; then
    return 0
  fi
  if git config --local --includes --get-regexp '^url\..*\.(insteadof|pushinsteadof)$' >/dev/null 2>&1; then
    return 1
  fi
  remote=$(git config --local --no-includes --get remote.origin.url 2>/dev/null || true)
  remote=$(printf '%s' "$remote" | sed 's#\.git$##; s#/$##')
  case "$remote" in
    "https://github.com/$repo"|"git@github.com:$repo"|"ssh://git@github.com/$repo") ;;
    *) return 1 ;;
  esac
  # A dirty checkout may have changed this installer, manifest, or source.
  # Compile the local tree instead of silently replacing it with a release
  # binary selected by modified metadata.
  if ! git -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.pager=cat status --porcelain --untracked-files=all >/dev/null 2>&1; then
    return 1
  fi
  if git -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.pager=cat diff --quiet --no-ext-diff --no-textconv HEAD -- . 2>/dev/null && \
     git -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.pager=cat diff --cached --quiet --no-ext-diff --no-textconv HEAD -- . 2>/dev/null && \
     [ -z "$(git -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.pager=cat status --porcelain --untracked-files=all 2>/dev/null | head -n 1)" ]; then
    return 0
  fi
  return 1
}

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) os="" ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) arch="" ;;
esac

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" 2>/dev/null | awk '{print tolower($1)}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" 2>/dev/null | awk '{print tolower($1)}'
  else
    return 1
  fi
}

ensure_output_is_not_link() {
  if [ -L "$bin" ]; then
    echo "herdr-gh-checks: refusing to overwrite symlink $bin" >&2
    exit 1
  fi
  if [ -e "$bin" ] && [ ! -f "$bin" ]; then
    echo "herdr-gh-checks: refusing to overwrite non-regular output $bin" >&2
    exit 1
  fi
}

build_from_source() {
  if command -v go >/dev/null 2>&1; then
    echo "herdr-gh-checks: building from source with go"
    # Build outside the destination, then rename. A check followed by `go
    # build -o ./herdr-gh-checks` could still follow a target symlink swapped
    # between the two operations.
    build_tmp=$(mktemp "./.${bin}.build.XXXXXX")
    if ! GOTOOLCHAIN=local go build -trimpath -ldflags "-s -w" -o "$build_tmp" .; then
      exit 1
    fi
    chmod 0755 "$build_tmp"
    ensure_output_is_not_link
    mv -f "$build_tmp" "./$bin"
    build_tmp=
    exit 0
  fi
  echo "herdr-gh-checks: no reviewed prebuilt binary for ${os:-?}/${arch:-?} and Go is not installed." >&2
  echo "  Install Go 1.25.14+ (to build from source) or use a supported platform (linux/macOS, amd64/arm64)." >&2
  exit 1
}

[ -n "$os" ] && [ -n "$arch" ] && [ -n "$version" ] && command -v curl >/dev/null 2>&1 || build_from_source
if ! printf '%s' "$version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$'; then
  echo "herdr-gh-checks: invalid manifest version; building from source" >&2
  build_from_source
fi
if ! source_repo_matches; then
  echo "herdr-gh-checks: checkout is not the canonical release repository; building from source" >&2
  build_from_source
fi

asset="${bin}-${os}-${arch}"
base="https://github.com/${repo}/releases/download/v${version}"
key="v${version}/${asset}"
want=""
if [ -f release-checksums.txt ]; then
  want=$(awk -v key="$key" '$1 !~ /^#/ && $1 == key {print tolower($2); exit}' release-checksums.txt)
fi
if ! printf '%s\n' "$want" | grep -Eq '^[0-9a-f]{64}$'; then
  echo "herdr-gh-checks: no reviewed digest for $key; building from source" >&2
  build_from_source
fi

tmp=$(mktemp -d)

if curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL --retry 3 --retry-delay 1 "$base/$asset" -o "$tmp/$bin"; then
  got=$(sha256 "$tmp/$bin" || true)
  if [ "$want" = "$got" ]; then
    ensure_output_is_not_link
    chmod 0755 "$tmp/$bin"
    mv "$tmp/$bin" "./$bin"
    echo "herdr-gh-checks: installed allowlisted prebuilt $asset v$version"
    exit 0
  fi
  echo "herdr-gh-checks: digest mismatch for $asset; falling back to source" >&2
fi
build_from_source

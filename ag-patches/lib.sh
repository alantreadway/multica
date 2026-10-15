# Shared by build-web.sh, build-cli.sh and test.sh. Source it; do not run it.

# ag_prepare <script-name> <artifact> [args...]
# Parses --rev and --skip-tests into $rev and $skip_tests, moves to the repo
# root, checks the tree is clean and on a patch branch ag/<tag>, and sets $tag
# and $version. Exits on any problem.
ag_prepare() {
  local script="$1" artifact="$2"
  shift 2

  rev=1
  skip_tests=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --rev)
        [ $# -ge 2 ] || ag_usage "$script"
        rev="$2"
        shift 2
        ;;
      --skip-tests)
        skip_tests=1
        shift
        ;;
      *) ag_usage "$script" ;;
    esac
  done

  case "$rev" in
    '' | *[!0-9]*)
      echo "error: --rev must be a non-negative integer, got '$rev'" >&2
      exit 2
      ;;
  esac

  cd "$(git rev-parse --show-toplevel)"

  if [ -n "$(git status --porcelain)" ]; then
    echo "error: working tree is dirty; commit or stash first so the ${artifact} matches HEAD" >&2
    exit 1
  fi

  local branch
  branch="$(git symbolic-ref --quiet --short HEAD || true)"
  case "$branch" in
    ag/*) tag="${branch#ag/}" ;;
    *)
      echo "error: must be on a patch branch named ag/<upstream-tag> (on '${branch:-detached HEAD}')" >&2
      exit 1
      ;;
  esac

  if ! git rev-parse --verify --quiet "refs/tags/${tag}^{commit}" >/dev/null; then
    echo "error: upstream tag '${tag}' not found; run: git fetch upstream --tags" >&2
    exit 1
  fi
  if ! git merge-base --is-ancestor "${tag}" HEAD; then
    echo "error: ${tag} is not an ancestor of HEAD; the branch is not based on that tag" >&2
    exit 1
  fi

  # Web and CLI builds share one counter, so a released ag/<version> tag may
  # already mark a different build.
  version="${tag}-ag.${rev}"
  local tagged
  tagged="$(git rev-parse --verify --quiet "refs/tags/ag/${version}^{commit}" || true)"
  if [ -n "$tagged" ] && [ "$tagged" != "$(git rev-parse HEAD)" ]; then
    echo "error: tag ag/${version} already marks ${tagged}; pick a higher --rev (web and CLI builds share the counter)" >&2
    exit 1
  fi
}

ag_usage() {
  echo "usage: ag-patches/$1 [--rev N] [--skip-tests]" >&2
  exit 2
}

# ag_guard_tests: run test.sh unless --skip-tests was given.
ag_guard_tests() {
  if [ "$skip_tests" -eq 0 ]; then
    ./ag-patches/test.sh
  else
    echo "warning: --skip-tests set; guard tests not run" >&2
  fi
}

# ag_go <go args...>: run `go` from server/ with local go, or in a golang
# container matching server/go.mod when go is not installed (or
# AG_FORCE_DOCKER is set). Variables named in AG_GO_ENV are passed through.
ag_go() {
  if [ -z "${AG_FORCE_DOCKER:-}" ] && command -v go >/dev/null 2>&1; then
    (cd server && go "$@")
  elif command -v docker >/dev/null 2>&1; then
    local go_version name envargs=()
    go_version="$(awk '/^go /{print $2; exit}' server/go.mod)"
    for name in ${AG_GO_ENV:-}; do envargs+=(-e "$name"); done
    echo "using golang:${go_version} via docker"
    docker run --rm \
      -v "$PWD":/src -w /src/server \
      -v multica-ag-gomod:/go/pkg/mod \
      ${envargs[@]+"${envargs[@]}"} \
      "golang:${go_version}" go "$@"
  else
    echo "error: need either go or docker to run the Go toolchain" >&2
    return 1
  fi
}

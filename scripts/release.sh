#!/usr/bin/env bash
# A release retry's own commands (ADR 0007; CONTRIBUTING.md, "When a release
# is red"). .github/workflows/release.yml runs plan, download, clear-draft and
# clear-winget when it is dispatched with a tag, refresh.yml runs wait after
# it tags a patch; a workstation runs the same, with jq, yq and an
# authenticated gh on PATH, and $GITHUB_REPOSITORY naming the repository.
#
#   scripts/release.sh plan [--resign] <tag> <skip> <dir>  what a retry of <tag> publishes; downloads the
#                                                         release's checksums.txt and bundles into <dir>
#                                                         and prints release=, flag=, ran=, skipped=,
#                                                         already= lines
#   scripts/release.sh download <tag> <dir> <asset>...   the named assets of <tag>'s release, into <dir>
#   scripts/release.sh clear-draft <tag>                 delete <tag>'s draft release, keeping the tag
#   scripts/release.sh skip [--bundled] [--resign] <list>   goreleaser's --skip flag for a retry of the
#                                                         publishers not in <list>; prints nothing when
#                                                         nothing is skipped
#   scripts/release.sh published <tag>                    the publishers whose channel already has <tag>,
#                                                         one per line
#   scripts/release.sh clear-winget <tag>                 delete the winget fork's branch of <tag> that a
#                                                         failed attempt left, unless a pull request has it
#   scripts/release.sh retry-skip <conclusion> <log>      the skip list that retries only what a failed
#                                                         run did not publish
#   scripts/release.sh wait <tag>                         wait for release.yml on <tag>, retry it once in
#                                                         parts; prints runs=<url> [<url>]
#
# <tag> is vX.Y.Z, or vX.Y.Z-<prerelease> (v1.0.0-rc.1), as release.yml takes.
#
# plan and the release: the retry completes the tag's published release, or
# its draft. goreleaser makes a release as a draft, uploads every asset and
# publishes it only when all of them are up, so an upload that failed leaves
# a draft, which nobody but the maintainers can see. goreleaser looks a
# release up by its tag (GET releases/tags/<tag>), which never answers with
# a draft: run again, it would make a second release beside the draft. So a
# draft is unpublished: plan compares the rebuild with what it holds like any
# release, and makes every bundle again; clear-draft, run after that
# comparison and before goreleaser, deletes it by id, leaving the tag, and
# goreleaser makes and publishes the release whole. A tag with no release,
# or with more than one draft and none published, is refused. The releases
# are read from the REST list, which has the drafts, by id; download fetches
# assets the same way.
#
# skip: <list> names the publishers a retry leaves alone, separated by commas
# or spaces: docker, homebrew, scoop, winget, and sign (goreleaser's signing
# of every asset). The GitHub release itself is not in the list: a retry
# always re-attaches to it, replacing assets by name with the same bytes.
# "publish" is refused, as is any other name, before goreleaser starts.
# --bundled says every asset the release names already has its bundle, and
# adds sign, so a retry makes no second set of signatures for the same bytes;
# --resign drops that default. Naming sign without --bundled is refused: the
# missing bundles would never be made.
#
# published asks each channel for the tag's version, so a retry of a
# publisher that has already published is a no-op: the Homebrew cask in the
# tap, the Scoop manifest in the bucket (both pushed with git, where
# goreleaser fails on an unchanged file rather than committing nothing) and a
# pull request of the winget branch to its upstream, in any state. The
# channels are .goreleaser.yaml's.
#
# clear-winget: goreleaser reuses a winget branch that exists, so a retry
# would build on whatever a failed attempt pushed there. With no pull
# request from it, the branch is deleted and the retry starts it again. Run
# with the winget token as GH_TOKEN.
#
# retry-skip reads a failed release.yml run: <conclusion> is its goreleaser
# step's, <log> its failed steps' log (gh run view --log-failed). goreleaser
# publishes in order: the image (docker), the GitHub release, then winget, the
# Homebrew cask and the Scoop manifest, which carry on past each other's
# failure and are listed together at the end. A goreleaser step that
# succeeded leaves only the steps after it to retry, so every publisher is
# skipped. A failed GitHub release skips docker. A failure of the image, or
# before publishing, has no release to complete and is refused, as is a log
# this cannot read: re-run the failed jobs, or fix and tag a new patch.
#
# wait: the refresh's last step. It waits for release.yml's run on the tag's
# push and, when that fails, dispatches release.yml from the tag with
# retry-skip's list and waits for that run too. Green either way exits 0; a
# retry that is refused or fails again exits 1. Each runs= line names every
# run so far (the last one counts). $RELEASE_POLL sets the seconds between
# polls (default 15).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PUBLISHERS=(docker homebrew scoop winget)
POLL=${RELEASE_POLL:-15}

die() { echo "release: $*" >&2; exit 1; }

usage() {
  sed -n '/^#   scripts/,/^#$/s/^#  //p' "$0" >&2
  exit 2
}

# The value of a .goreleaser.yaml path.
config() { yq -r "$1" "$ROOT/.goreleaser.yaml"; }

version_of() {
  [[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "$1 is not a release tag (vX.Y.Z or vX.Y.Z-<prerelease>)"
  echo "${1#v}"
}

# The tag's release, as "<id> published" or "<id> draft".
release_of() {
  local tag=$1 list id draft published=() drafts=()
  version_of "$tag" >/dev/null
  list=$(gh api --paginate "repos/${GITHUB_REPOSITORY:?}/releases?per_page=100" \
    --jq ".[] | select(.tag_name == \"$tag\") | \"\(.id) \(.draft)\"")
  while read -r id draft; do
    case $draft in
      true) drafts+=("$id") ;;
      false) published+=("$id") ;;
    esac
  done <<<"$list"
  if [ ${#published[@]} -gt 0 ]; then
    echo "${published[0]} published"
  elif [ ${#drafts[@]} -eq 1 ]; then
    echo "${drafts[0]} draft"
  elif [ ${#drafts[@]} -gt 1 ]; then
    die "$tag has ${#drafts[@]} draft releases (${drafts[*]}) and none published: delete all but the newest on the releases page"
  else
    die "$tag has no release, so nothing was published to complete: re-run the tag's failed jobs instead"
  fi
}

# The assets of release <id>, one "<asset id> <name>" per line.
assets_of() {
  gh api --paginate "repos/$GITHUB_REPOSITORY/releases/$1/assets?per_page=100" --jq '.[] | "\(.id) \(.name)"'
}

# Download the named assets, from the "<asset id> <name>" lines <assets>,
# into <dir>.
fetch() {
  local assets=$1 dir=$2 name aid
  shift 2
  mkdir -p "$dir"
  for name in "$@"; do
    aid=$(awk -v n="$name" '$2 == n {print $1}' <<<"$assets")
    [ -n "$aid" ] || die "download: the release has no $name"
    gh api -H 'Accept: application/octet-stream' "repos/$GITHUB_REPOSITORY/releases/assets/$aid" >"$dir/$name"
  done
}

cmd_download() {
  [ $# -ge 3 ] || usage
  local tag=$1 dir=$2 rel id state
  shift 2
  rel=$(release_of "$tag")
  read -r id state <<<"$rel"
  fetch "$(assets_of "$id")" "$dir" "$@"
}

cmd_plan() {
  local resign=false
  [ "${1-}" != --resign ] || { resign=true; shift; }
  [ $# -eq 3 ] || usage
  local tag=$1 skip=$2 dir=$3 rel id state assets asset get=() opts=(--bundled) already flag skipped name ran=(release)
  rel=$(release_of "$tag")
  read -r id state <<<"$rel"
  assets=$(assets_of "$id")
  mkdir -p "$dir"
  awk 'NF {print $2}' <<<"$assets" | sort >"$dir/assets.txt"
  while read -r asset; do
    case $asset in checksums.txt | *.sigstore.json) get+=("$asset") ;; esac
  done <"$dir/assets.txt"
  [ ${#get[@]} -eq 0 ] || fetch "$assets" "$dir" "${get[@]}"
  if [ "$state" = draft ]; then
    echo "release: $tag's release is a draft, a failed upload's: it is deleted after the comparison and made again, so signing runs" >&2
    opts=()
  elif [ -f "$dir/checksums.txt" ]; then
    # Every asset checksums.txt names, and checksums.txt, has its bundle.
    while read -r asset; do
      [ -f "$dir/$asset.sigstore.json" ] || { echo "release: $asset has no bundle: signing runs" >&2; opts=(); }
    done < <(awk '{print $2}' "$dir/checksums.txt"; echo checksums.txt)
  else
    echo "release: the release has no checksums.txt: signing runs" >&2
    opts=()
  fi
  ! $resign || opts+=(--resign)
  already=$(cmd_published "$tag" | paste -sd, -)
  flag=$(cmd_skip ${opts[@]+"${opts[@]}"} "$skip,$already")
  skipped=${flag#--skip=}
  for name in "${PUBLISHERS[@]}" sign; do
    [[ ,$skipped, == *,$name,* ]] || ran+=("$name")
  done
  echo "release=$state"
  echo "flag=$flag"
  (IFS=,; echo "ran=${ran[*]}")
  echo "skipped=$skipped"
  echo "already=$already"
}

cmd_clear_draft() {
  [ $# -eq 1 ] || usage
  local rel id state
  rel=$(release_of "$1")
  read -r id state <<<"$rel"
  [ "$state" = draft ] || die "clear-draft: $1's release $id is published; it is completed, never deleted"
  gh api -X DELETE "repos/$GITHUB_REPOSITORY/releases/$id" >/dev/null
  echo "release: deleted $1's draft release $id, left by a failed upload; the tag stays" >&2
}

winget_branch() {
  local template
  template=$(config '.winget[0].repository.branch')
  [[ $template == *'{{ .Version }}'* ]] || die ".goreleaser.yaml's winget branch $template does not name {{ .Version }}"
  echo "${template//'{{ .Version }}'/$1}"
}

cmd_skip() {
  local bundled=false resign=false list name skipped=" " out=()
  while [ $# -gt 0 ]; do
    case $1 in
      --bundled) bundled=true ;;
      --resign) resign=true ;;
      -*) usage ;;
      *) break ;;
    esac
    shift
  done
  [ $# -le 1 ] || usage
  list=${1-}
  IFS=$', \t' read -ra names <<<"$list"
  for name in ${names[@]+"${names[@]}"}; do
    case $name in
      "") ;;
      publish) die "skip: publish skips every publisher: a retry would publish nothing; leave out the names to retry" ;;
      docker | homebrew | scoop | winget | sign) skipped+="$name " ;;
      *) die "skip: no publisher $name; the names are ${PUBLISHERS[*]} and sign" ;;
    esac
  done
  if [[ $skipped == *" sign "* ]]; then
    $bundled || die "skip: sign, but not every asset of the release has its bundle; leave sign out"
    ! $resign || die "skip: sign, and --resign"
  elif $bundled && ! $resign; then
    skipped+="sign "
  fi
  for name in "${PUBLISHERS[@]}" sign; do
    [[ $skipped == *" $name "* ]] && out+=("$name")
  done
  if [ ${#out[@]} -gt 0 ]; then
    local IFS=,
    echo "--skip=${out[*]}"
  fi
}

cmd_published() {
  [ $# -eq 1 ] || usage
  local version repo dir file content branch fork upstream prs
  version=$(version_of "$1")

  repo=$(config '.homebrew_casks[0].repository | .owner + "/" + .name')
  dir=$(config '.homebrew_casks[0].directory // "Casks"')
  file="$dir/$(config '.homebrew_casks[0].name').rb"
  # Read whole, not piped into grep -q, whose early exit would fail gh.
  content=$(gh api -H 'Accept: application/vnd.github.raw' "repos/$repo/contents/$file") || content=
  if grep -qxF "  version \"$version\"" <<<"$content"; then
    echo "release: $repo's $file is at $version" >&2
    echo homebrew
  fi

  repo=$(config '.scoops[0].repository | .owner + "/" + .name')
  file="$(config '.scoops[0].directory')/$(config '.scoops[0].name').json"
  content=$(gh api -H 'Accept: application/vnd.github.raw' "repos/$repo/contents/$file") || content=
  if jq -e --arg v "$version" '.version == $v' <<<"$content" >/dev/null 2>&1; then
    echo "release: $repo's $file is at $version" >&2
    echo scoop
  fi

  branch=$(winget_branch "$version")
  fork=$(config '.winget[0].repository.owner')
  upstream=$(config '.winget[0].repository.pull_request.base | .owner + "/" + .name')
  prs=$(gh api "repos/$upstream/pulls?head=$fork:$branch&state=all" --jq 'map("#\(.number) (\(.state))") | join(", ")')
  if [ -n "$prs" ]; then
    echo "release: $upstream has $fork:$branch as $prs" >&2
    echo winget
  fi
}

cmd_clear_winget() {
  [ $# -eq 1 ] || usage
  local version branch fork upstream prs
  version=$(version_of "$1")
  branch=$(winget_branch "$version")
  fork=$(config '.winget[0].repository | .owner + "/" + .name')
  upstream=$(config '.winget[0].repository.pull_request.base | .owner + "/" + .name')
  if ! gh api "repos/$fork/branches/$branch" --jq .name >/dev/null 2>&1; then
    echo "release: $fork has no branch $branch" >&2
    return
  fi
  prs=$(gh api "repos/$upstream/pulls?head=${fork%%/*}:$branch&state=all" --jq 'map("#\(.number)") | join(", ")')
  [ -z "$prs" ] || die "clear-winget: $upstream has $fork:$branch as $prs; skip winget"
  gh api -X DELETE "repos/$fork/git/refs/heads/$branch" >/dev/null
  echo "release: deleted $fork's branch $branch, left by a failed attempt" >&2
}

cmd_retry_skip() {
  [ $# -eq 2 ] || usage
  local conclusion=$1 log name failed=" " out=()
  case $conclusion in
    success)
      local IFS=,
      echo "${PUBLISHERS[*]}"
      return
      ;;
    failure) ;;
    *) die "retry-skip: goreleaser's step is ${conclusion:-absent}, so nothing was published to complete" ;;
  esac
  # The log as goreleaser wrote it, without its colours.
  log=$(sed "s/$(printf '\033')\[[0-9;]*m//g" "$2")
  grep -q 'release failed after' <<<"$log" || die "retry-skip: the log has no goreleaser failure"
  if grep -qF 'scm releases: failed to publish artifacts' <<<"$log"; then
    echo docker
    return
  fi
  grep -qE '(docker images \(v2\)|docker digests): failed to publish artifacts' <<<"$log" &&
    die "retry-skip: the image failed, before the GitHub release; re-run the failed jobs"
  while read -r name; do
    case $name in
      'homebrew cask') failed+="homebrew " ;;
      'scoop manifests') failed+="scoop " ;;
      winget) failed+="winget " ;;
    esac
  done < <(grep -oE '\* (winget|homebrew cask|scoop manifests): ' <<<"$log" | sed -E 's/^\* (.*): $/\1/')
  [ "$failed" != " " ] || die "retry-skip: no publisher failed: goreleaser failed before publishing"
  for name in "${PUBLISHERS[@]}"; do
    [[ $failed == *" $name "* ]] || out+=("$name")
  done
  local IFS=,
  echo "${out[*]}"
}

# The id of release.yml's newest run on <tag> by <event> with an id above
# <after>, once it exists.
find_run() {
  local event=$1 tag=$2 after=$3 id
  for _ in $(seq 40); do
    id=$(gh run list --workflow release.yml --event "$event" --branch "$tag" -L 1 --json databaseId \
      --jq ".[0].databaseId // 0 | select(. > $after)")
    [ -n "$id" ] && { echo "$id"; return; }
    sleep "$POLL"
  done
  die "wait: release.yml did not start on $tag ($event)"
}

cmd_wait() {
  [ $# -eq 1 ] || usage
  local tag=$1 first second url1 url2 before conclusion skip
  local runs="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:?}/actions/runs"
  first=$(find_run push "$tag" 0)
  url1="$runs/$first"
  echo "runs=$url1"
  if gh run watch "$first" --exit-status --interval 30 >&2; then
    gh release view "$tag" >&2
    return
  fi
  conclusion=$(gh run view "$first" --json jobs \
    --jq '.jobs[] | select(.name == "release") | .steps[] | select(.name == "goreleaser") | .conclusion')
  # Global, for the trap.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  gh run view "$first" --log-failed >"$tmp/log" || true
  skip=$(cmd_retry_skip "$conclusion" "$tmp/log") || die "wait: $url1 failed and cannot be retried in parts"
  before=$(gh run list --workflow release.yml --event workflow_dispatch --branch "$tag" -L 1 --json databaseId --jq '.[0].databaseId // 0')
  echo "release: $url1 failed; retrying $tag with skip=$skip" >&2
  gh workflow run release.yml --ref "$tag" -f tag="$tag" -f skip="$skip" >&2
  second=$(find_run workflow_dispatch "$tag" "$before")
  url2="$runs/$second"
  echo "runs=$url1 $url2"
  gh run watch "$second" --exit-status --interval 30 >&2 || die "wait: the retry $url2 failed too, after $url1"
  gh release view "$tag" >&2
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case $cmd in
  plan) cmd_plan "$@" ;;
  download) cmd_download "$@" ;;
  clear-draft) cmd_clear_draft "$@" ;;
  skip) cmd_skip "$@" ;;
  published) cmd_published "$@" ;;
  clear-winget) cmd_clear_winget "$@" ;;
  retry-skip) cmd_retry_skip "$@" ;;
  wait) cmd_wait "$@" ;;
  *) usage ;;
esac

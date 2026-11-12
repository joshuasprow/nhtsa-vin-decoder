// Tests of scripts/release.sh, the release retry's helper, at its boundary:
// the arguments it is given, what it prints, and the gh calls it makes,
// against a fake gh that answers from files and records every call. Run by
// release.yml's snapshot job; they need bash, jq and yq on PATH, as the
// script does.
package scripts

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH stands in for gh. It appends each call's arguments to $FAKE/calls
// and answers from $FAKE:
//   - run list --event <e>: runs-<e>.json, or runs-dispatched.json once a
//     workflow run has been called;
//   - run watch <id>: exits with the number in watch-<id>;
//   - run view <id> --log-failed: log-<id>; run view <id>: jobs-<id>.json;
//   - workflow run: records the dispatch;
//   - release view: succeeds;
//   - api [-X <method>] <path>: api/<method>/<path>, or fails as a 404.
//
// --jq filters the answer through jq, as gh does.
const fakeGH = `#!/usr/bin/env bash
set -euo pipefail
echo "$*" >>"$FAKE/calls"
filter= method=GET event= path= log=false
args=()
while [ $# -gt 0 ]; do
  case $1 in
    --jq) filter=$2; shift 2 ;;
    -X) method=$2; shift 2 ;;
    -H) shift 2 ;;
    --event) event=$2; shift 2 ;;
    --log-failed) log=true; shift ;;
    *) args+=("$1"); shift ;;
  esac
done
set -- "${args[@]}"
reply() {
  [ -f "$FAKE/$1" ] || { echo "gh: HTTP 404: Not Found ($1)" >&2; exit 1; }
  if [ -n "$filter" ]; then jq -r "$filter" "$FAKE/$1"; else cat "$FAKE/$1"; fi
}
case "$1 $2" in
  "run list")
    if [ "$event" = workflow_dispatch ] && [ -f "$FAKE/dispatched" ]; then reply runs-dispatched.json; else reply "runs-$event.json"; fi ;;
  "run watch") exit "$(cat "$FAKE/watch-$3")" ;;
  "run view")
    if $log; then reply "log-$3"; else reply "jobs-$3.json"; fi ;;
  "workflow run") touch "$FAKE/dispatched" ;;
  "release view") echo "release $3" ;;
  api\ *) reply "api/$method/$2" ;;
  *) echo "gh: unexpected call: $*" >&2; exit 1 ;;
esac
`

type script struct {
	t    *testing.T
	fake string // the fake gh's answers and calls
}

// newScript puts the fake gh first on PATH.
func newScript(t *testing.T, files map[string]string) *script {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "yq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("scripts/release.sh needs %s on PATH", tool)
		}
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &script{t: t, fake: t.TempDir()}
	for name, content := range files {
		path := filepath.Join(s.fake, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE", s.fake)
	t.Setenv("RELEASE_POLL", "0")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "owner/nitrovin")
	return s
}

// run runs scripts/release.sh with args; failed reports a non-zero exit.
func (s *script) run(args ...string) (stdout, stderr string, failed bool) {
	s.t.Helper()
	cmd := exec.Command("bash", append([]string{"release.sh"}, args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		s.t.Fatal(err)
	}
	return out.String(), errOut.String(), err != nil
}

// calls are the fake gh's calls, one per line.
func (s *script) calls() []string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.fake, "calls"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func (s *script) callsWith(prefix string) []string {
	var got []string
	for _, c := range s.calls() {
		if strings.HasPrefix(c, prefix) {
			got = append(got, c)
		}
	}
	return got
}

func TestSkip(t *testing.T) {
	s := newScript(t, nil)
	for _, tc := range []struct {
		name string
		args []string
		want string // stdout, or the error's text when fail
		fail bool
	}{
		{"empty list, bundles missing", []string{""}, "", false},
		{"empty list, every bundle", []string{"--bundled", ""}, "--skip=sign\n", false},
		{"in goreleaser's order", []string{"--bundled", "winget, homebrew docker"}, "--skip=docker,homebrew,winget,sign\n", false},
		{"duplicates once", []string{"scoop,scoop,,"}, "--skip=scoop\n", false},
		{"resign keeps signing", []string{"--bundled", "--resign", "scoop"}, "--skip=scoop\n", false},
		{"sign named, every bundle", []string{"--bundled", "sign,docker"}, "--skip=docker,sign\n", false},
		{"every publisher", []string{"--bundled", "docker,homebrew,scoop,winget"}, "--skip=docker,homebrew,scoop,winget,sign\n", false},
		{"publish refused", []string{"--bundled", "publish"}, "publish skips every publisher", true},
		{"unknown name refused", []string{"homebrew,wingett"}, "no publisher wingett", true},
		{"goreleaser name refused", []string{"archive"}, "no publisher archive", true},
		{"sign without every bundle refused", []string{"sign"}, "not every asset of the release has its bundle", true},
		{"sign with resign refused", []string{"--bundled", "--resign", "sign"}, "sign, and --resign", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, failed := s.run(append([]string{"skip"}, tc.args...)...)
			if failed != tc.fail {
				t.Fatalf("failed = %v, want %v; stdout %q, stderr %q", failed, tc.fail, out, errOut)
			}
			if tc.fail {
				if out != "" || !strings.Contains(errOut, tc.want) {
					t.Errorf("stdout %q, stderr %q; want nothing and an error naming %q", out, errOut, tc.want)
				}
			} else if out != tc.want {
				t.Errorf("stdout %q, want %q (stderr %q)", out, tc.want, errOut)
			}
		})
	}
}

// goreleaserLog is a failed goreleaser step as gh run view --log-failed
// prints it: each line prefixed with the job, the step and a time, and
// goreleaser's colours kept.
func goreleaserLog(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("release\tgoreleaser\t2026-11-09T20:51:15.0000000Z " + l + "\n")
	}
	return b.String()
}

var (
	published = []string{
		"\x1b[1;94m  •\x1b[m \x1b[1mpublishing\x1b[m",
		"\x1b[1;94m    •\x1b[m \x1b[1mdocker images (v2)\x1b[m",
		"\x1b[1;94m    •\x1b[m \x1b[1mscm releases\x1b[m",
		"\x1b[1;94m    •\x1b[m \x1b[1mwinget\x1b[m",
		"\x1b[1;94m    •\x1b[m \x1b[1mhomebrew cask\x1b[m",
		"\x1b[1;94m    •\x1b[m \x1b[1mscoop manifests\x1b[m",
	}
	failedAfter = "\x1b[1;91m  ⨯\x1b[m release failed after 4m50s"
)

func failure(errLines ...string) string {
	lines := append(append([]string{}, published...), failedAfter, "    \x1b[1;91merror\x1b[m=")
	for _, l := range errLines {
		lines = append(lines, "  \x1b[1;91m  │ \x1b[m"+l)
	}
	return goreleaserLog(lines...)
}

func TestRetrySkip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		conclusion string
		log        string
		want       string // stdout, or the error's text when fail
		fail       bool
	}{
		{"winget failed", "failure", failure("1 error occurred:", "\t* winget: expected {{ .Env.VAR_NAME }} only"), "docker,homebrew,scoop\n", false},
		{"winget and scoop failed", "failure", failure("2 errors occurred:", "\t* winget: 502 Bad Gateway", "\t* scoop manifests: git: failed to push"), "docker,homebrew\n", false},
		{"every channel failed", "failure", failure("3 errors occurred:", "\t* winget: x", "\t* homebrew cask: y", "\t* scoop manifests: z"), "docker\n", false},
		{"the GitHub release failed", "failure", failure("scm releases: failed to publish artifacts: could not release: 502"), "docker\n", false},
		{"goreleaser green, a later step red", "success", "", "docker,homebrew,scoop,winget\n", false},
		{"the image failed", "failure", failure("docker images (v2): failed to publish artifacts: push: 500"), "the image failed", true},
		{"failed before publishing", "failure", goreleaserLog(failedAfter, "    error=hook failed: scripts/epa.sh"), "goreleaser failed before publishing", true},
		{"no goreleaser failure in the log", "failure", goreleaserLog("cosign: network unreachable"), "no goreleaser failure", true},
		{"goreleaser never ran", "skipped", "", "nothing was published", true},
		{"no release job", "", "", "nothing was published", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScript(t, map[string]string{"log": tc.log})
			out, errOut, failed := s.run("retry-skip", tc.conclusion, filepath.Join(s.fake, "log"))
			if failed != tc.fail {
				t.Fatalf("failed = %v, want %v; stdout %q, stderr %q", failed, tc.fail, out, errOut)
			}
			if tc.fail {
				if out != "" || !strings.Contains(errOut, tc.want) {
					t.Errorf("stdout %q, stderr %q; want nothing and an error naming %q", out, errOut, tc.want)
				}
			} else if out != tc.want {
				t.Errorf("stdout %q, want %q (stderr %q)", out, tc.want, errOut)
			}
		})
	}
}

func jobs(goreleaser string) string {
	return `{"jobs":[{"name":"bench","steps":[{"name":"Benchmark","conclusion":"success"}]},` +
		`{"name":"release","steps":[{"name":"goreleaser","conclusion":"` + goreleaser + `"}]}]}`
}

func TestWait(t *testing.T) {
	const (
		tag    = "v0.9.1"
		first  = "https://github.com/owner/nitrovin/actions/runs/101"
		second = "https://github.com/owner/nitrovin/actions/runs/202"
	)
	wingetFailed := failure("1 error occurred:", "\t* winget: 502 Bad Gateway")
	for _, tc := range []struct {
		name     string
		files    map[string]string
		runs     string // the last runs= line
		dispatch string // the one workflow run call, or none
		fail     bool
		stderr   string
	}{
		{
			name:  "green at once",
			files: map[string]string{"runs-push.json": `[{"databaseId":101}]`, "watch-101": "0"},
			runs:  first,
		},
		{
			name: "red at winget, the retry green",
			files: map[string]string{
				"runs-push.json": `[{"databaseId":101}]`, "watch-101": "1",
				"jobs-101.json": jobs("failure"), "log-101": wingetFailed,
				"runs-workflow_dispatch.json": `[{"databaseId":55}]`, "runs-dispatched.json": `[{"databaseId":202}]`, "watch-202": "0",
			},
			runs:     first + " " + second,
			dispatch: "workflow run release.yml --ref v0.9.1 -f tag=v0.9.1 -f skip=docker,homebrew,scoop",
		},
		{
			name: "red after goreleaser, the retry skips every publisher",
			files: map[string]string{
				"runs-push.json": `[{"databaseId":101}]`, "watch-101": "1",
				"jobs-101.json": jobs("success"), "log-101": goreleaserLog("release\tPublish to the MCP Registry\t... 503"),
				"runs-workflow_dispatch.json": `[]`, "runs-dispatched.json": `[{"databaseId":202}]`, "watch-202": "0",
			},
			runs:     first + " " + second,
			dispatch: "workflow run release.yml --ref v0.9.1 -f tag=v0.9.1 -f skip=docker,homebrew,scoop,winget",
		},
		{
			name: "red twice",
			files: map[string]string{
				"runs-push.json": `[{"databaseId":101}]`, "watch-101": "1",
				"jobs-101.json": jobs("failure"), "log-101": wingetFailed,
				"runs-workflow_dispatch.json": `[]`, "runs-dispatched.json": `[{"databaseId":202}]`, "watch-202": "1",
			},
			runs:     first + " " + second,
			dispatch: "workflow run release.yml --ref v0.9.1 -f tag=v0.9.1 -f skip=docker,homebrew,scoop",
			fail:     true,
			stderr:   "the retry " + second + " failed too, after " + first,
		},
		{
			name: "red at the image: no retry",
			files: map[string]string{
				"runs-push.json": `[{"databaseId":101}]`, "watch-101": "1",
				"jobs-101.json": jobs("failure"), "log-101": failure("docker images (v2): failed to publish artifacts: 500"),
			},
			runs:   first,
			fail:   true,
			stderr: first + " failed and cannot be retried in parts",
		},
		{
			name: "the dispatched run never starts",
			files: map[string]string{
				"runs-push.json": `[{"databaseId":101}]`, "watch-101": "1",
				"jobs-101.json": jobs("failure"), "log-101": wingetFailed,
				"runs-workflow_dispatch.json": `[{"databaseId":55}]`, "runs-dispatched.json": `[{"databaseId":55}]`,
			},
			runs:     first,
			dispatch: "workflow run release.yml --ref v0.9.1 -f tag=v0.9.1 -f skip=docker,homebrew,scoop",
			fail:     true,
			stderr:   "release.yml did not start on v0.9.1 (workflow_dispatch)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScript(t, tc.files)
			out, errOut, failed := s.run("wait", tag)
			if failed != tc.fail {
				t.Fatalf("failed = %v, want %v; stdout %q, stderr %q", failed, tc.fail, out, errOut)
			}
			var runs string
			for l := range strings.Lines(out) {
				l = strings.TrimSuffix(l, "\n")
				if !strings.HasPrefix(l, "runs=") {
					t.Errorf("stdout has %q: only runs= lines go to $GITHUB_OUTPUT", l)
				}
				runs = strings.TrimPrefix(l, "runs=")
			}
			if runs != tc.runs {
				t.Errorf("runs = %q, want %q", runs, tc.runs)
			}
			dispatched := s.callsWith("workflow run")
			switch {
			case tc.dispatch == "" && len(dispatched) > 0:
				t.Errorf("dispatched %q, want no retry", dispatched)
			case tc.dispatch != "" && (len(dispatched) != 1 || dispatched[0] != tc.dispatch):
				t.Errorf("dispatched %q, want exactly %q", dispatched, tc.dispatch)
			}
			if !strings.Contains(errOut, tc.stderr) {
				t.Errorf("stderr %q does not say %q", errOut, tc.stderr)
			}
		})
	}
}

func TestPublished(t *testing.T) {
	const (
		cask    = "api/GET/repos/joshuasprow/homebrew-tap/contents/Casks/nitrovin.rb"
		bucket  = "api/GET/repos/joshuasprow/scoop-bucket/contents/bucket/nitrovin.json"
		pulls   = "api/GET/repos/microsoft/winget-pkgs/pulls?head=joshuasprow:JoshuaSprow.Nitrovin-0.9.1&state=all"
		caskAt  = "cask \"nitrovin\" do\n  version \"%s\"\n\n  on_macos do\nend\n"
		atNine1 = "0.9.1"
	)
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"nothing published", map[string]string{
			cask: strings.Replace(caskAt, "%s", "0.9.0", 1), bucket: `{"version":"0.9.0"}`, pulls: `[]`,
		}, ""},
		{"tap and bucket at the version", map[string]string{
			cask: strings.Replace(caskAt, "%s", atNine1, 1), bucket: `{"version":"0.9.1"}`, pulls: `[]`,
		}, "homebrew\nscoop\n"},
		{"a later version is not this one", map[string]string{
			cask: strings.Replace(caskAt, "%s", "0.9.10", 1), bucket: `{"version":"0.9.10"}`, pulls: `[]`,
		}, ""},
		{"a winget pull request, closed", map[string]string{
			cask: strings.Replace(caskAt, "%s", "0.9.0", 1), bucket: `{"version":"0.9.0"}`, pulls: `[{"number":7,"state":"closed"}]`,
		}, "winget\n"},
		{"no channel answers", map[string]string{pulls: `[]`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScript(t, tc.files)
			out, errOut, failed := s.run("published", "v0.9.1")
			if failed || out != tc.want {
				t.Errorf("published v0.9.1 = %q (failed %v), want %q; stderr %q", out, failed, tc.want, errOut)
			}
		})
	}
	s := newScript(t, nil)
	if _, errOut, failed := s.run("published", "0.9.1"); !failed || !strings.Contains(errOut, "not a release tag") {
		t.Errorf("published 0.9.1: failed %v, stderr %q; want a refusal", failed, errOut)
	}
}

func TestClearWinget(t *testing.T) {
	const (
		branch = "api/GET/repos/joshuasprow/winget-pkgs/branches/JoshuaSprow.Nitrovin-0.9.1"
		pulls  = "api/GET/repos/microsoft/winget-pkgs/pulls?head=joshuasprow:JoshuaSprow.Nitrovin-0.9.1&state=all"
		del    = "api -X DELETE repos/joshuasprow/winget-pkgs/git/refs/heads/JoshuaSprow.Nitrovin-0.9.1"
	)
	for _, tc := range []struct {
		name    string
		files   map[string]string
		deleted bool
		fail    bool
	}{
		{"no branch", map[string]string{pulls: `[]`}, false, false},
		{"a branch a failed attempt left", map[string]string{
			branch: `{"name":"JoshuaSprow.Nitrovin-0.9.1"}`, pulls: `[]`,
			"api/DELETE/repos/joshuasprow/winget-pkgs/git/refs/heads/JoshuaSprow.Nitrovin-0.9.1": "",
		}, true, false},
		{"a branch in a pull request", map[string]string{
			branch: `{"name":"JoshuaSprow.Nitrovin-0.9.1"}`, pulls: `[{"number":7,"state":"open"}]`,
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScript(t, tc.files)
			out, errOut, failed := s.run("clear-winget", "v0.9.1")
			if failed != tc.fail {
				t.Fatalf("failed = %v, want %v; stdout %q, stderr %q", failed, tc.fail, out, errOut)
			}
			if got := len(s.callsWith(del)) == 1; got != tc.deleted {
				t.Errorf("deleted = %v, want %v; calls %q", got, tc.deleted, s.calls())
			}
		})
	}
}

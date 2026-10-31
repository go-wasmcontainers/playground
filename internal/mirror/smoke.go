package mirror

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-wasmcontainers/engine/image"
	"github.com/go-wasmcontainers/engine/rootfs"
	"github.com/go-wasmcontainers/engine/runtime"
	"github.com/go-wasmcontainers/engine/runtime/wazero"
)

// Result is one run of one module.
type Result struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
	// Err is a failure to start or a trap, not a non-zero exit.
	Err string `json:"err,omitempty"`
}

// Run is one invocation, under our engine and, when available, wasmtime.
type Run struct {
	Args     []string `json:"args"`
	Stdin    string   `json:"stdin,omitempty"`
	Engine   Result   `json:"engine"`
	Wasmtime *Result  `json:"wasmtime,omitempty"`
}

// SmokeReport is the smoke test of one image.
type SmokeReport struct {
	Image   string `json:"image"`
	Project string `json:"project"`
	Version string `json:"version"`
	Runs    []Run  `json:"runs"`
	// Verdict is "ok" when a run exits 0 with some output under our engine,
	// otherwise "fail".
	Verdict string `json:"verdict"`
	// Diffs name each stream or status that differs between the engine and
	// wasmtime, byte for byte. A difference is a finding, not noise.
	Diffs []string `json:"diffs,omitempty"`
}

// Probe is one invocation: arguments and standard input.
type Probe struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
	// AllowEmpty accepts exit 0 with no output (a validator that prints
	// nothing for a valid input: xmlwf).
	AllowEmpty bool `json:"allow_empty,omitempty"`
}

// DefaultProbes are run for every image, before its own probes: --version, then
// --help. Both are always run (a module may answer one only).
var DefaultProbes = []Probe{{Args: []string{"--version"}}, {Args: []string{"--help"}}}

// Spec is the committed smoke specification: per-image probes where
// --version/--help is not how the program answers (lua: -v), and the named
// expected failures and differences.
type Spec struct {
	Probes   map[string][]Probe     `json:"probes"`
	Expected map[string]Expectation `json:"expected"`
	// Varying names images whose published bytes are known to change under the
	// same tag (two build hosts publish it: go-pkgx/packages#393). The smoke
	// compares behaviour of the bytes it fetched, never their digest, and the
	// mirror keeps no per-(project, version) cache that could take a change for
	// corruption. It is not a finding.
	Varying map[string]string `json:"varying,omitempty"`
}

// SmokeImages smoke-tests the pkgx images (those with a Project) and sets
// each one's Checked: true only for a clean pass (every probe passes under the
// engine, no difference from wasmtime) of an image the spec does not list as
// expected. It returns the reports and a finding per image that is not clean.
func SmokeImages(ctx context.Context, imgs []Image, spec Spec, engine, wt Executor, lim Limits) ([]SmokeReport, []Finding) {
	var reps []SmokeReport
	var fs []Finding
	for i := range imgs {
		if imgs[i].Project == "" {
			continue
		}
		r := Smoke(ctx, imgs[i], spec.Probes[imgs[i].Name], engine, wt, lim)
		reps = append(reps, r)
		_, expected := spec.Expected[imgs[i].Name]
		clean := r.Verdict == "ok" && len(r.Diffs) == 0 && !expected
		imgs[i].Checked = &clean
		if clean {
			continue
		}
		kind := "smoke-" + r.Verdict
		if r.Verdict == "ok" {
			kind = "smoke-diff"
		}
		detail := strings.Join(r.Diffs, "; ")
		if e, ok := spec.Expected[imgs[i].Name]; ok {
			kind, detail = "smoke-expected", e.Verdict+": "+e.Reason
		}
		fs = append(fs, Finding{Project: imgs[i].Project, Image: imgs[i].Name, Kind: kind, Detail: detail})
	}
	return reps, fs
}

// Executor runs a module once. Tests replace it.
type Executor interface {
	Name() string
	Run(ctx context.Context, img Image, module []byte, p Probe) Result
}

// Native runs a module under the engine's wazero executor with a root file
// system built from the image's layer, exactly as the playground does.
type Native struct{ EngineWASI bool }

func (Native) Name() string { return "engine" }

func (n Native) Run(ctx context.Context, img Image, _ []byte, pr Probe) Result {
	mt := image.MediaTypeOCILayerGz
	if img.LayerZstd {
		mt = image.MediaTypeOCILayerZst
	}
	fs, err := rootfs.Build([]rootfs.LayerReader{{MediaType: mt, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(img.Layer)), nil }}}, rootfs.DefaultLimits)
	if err != nil {
		return Result{Exit: -1, Err: "rootfs: " + err.Error()}
	}
	e := wazero.New(wazero.Options{EngineWASI: n.EngineWASI})
	defer e.Close(ctx)
	var out, errb bytes.Buffer
	p, err := e.Start(ctx, runtime.Spec{FS: fs.Clone(rootfs.Quota{}), Path: img.ModulePath,
		Args: append([]string{img.Entrypoint[0]}, pr.Args...), Env: []string{"PWD=/"},
		Stdin: strings.NewReader(pr.Stdin), Stdout: &out, Stderr: &errb, Timeout: 30 * time.Second, MemoryLimitBytes: 512 << 20})
	if err != nil {
		return Result{Exit: -1, Err: "start: " + err.Error()}
	}
	r := p.Wait()
	return Result{Stdout: out.String(), Stderr: errb.String(), Exit: r.ExitCode, Err: r.Error}
}

// Wasmtime runs the module under the wasmtime CLI, stdin from /dev/null, a pipe
// for stdout and stderr (so isatty is false), the same environment.
type Wasmtime struct{ Bin string }

func (Wasmtime) Name() string { return "wasmtime" }

func (w Wasmtime) Run(ctx context.Context, img Image, module []byte, pr Probe) Result {
	dir, err := os.MkdirTemp("", "smoke-")
	if err != nil {
		return Result{Exit: -1, Err: err.Error()}
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, path.Base(img.ModulePath))
	if err := os.WriteFile(f, module, 0o644); err != nil {
		return Result{Exit: -1, Err: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// An empty directory preopened as "/", as the engine preopens its root
	// file system: without it a program that opens a relative path (a config
	// file) gets EBADF under wasmtime and not under the engine.
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		return Result{Exit: -1, Err: err.Error()}
	}
	cmd := exec.CommandContext(ctx, w.Bin, append([]string{"run", "--dir", root + "::/", "--argv0", img.Entrypoint[0], "--env", "PWD=/", f}, pr.Args...)...)
	cmd.Stdin = strings.NewReader(pr.Stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.Exit = ee.ExitCode()
		} else {
			res.Exit, res.Err = -1, err.Error()
		}
	}
	return res
}

// ExtractFile reads one regular file out of an image's layer, bounded.
func ExtractFile(img Image, p string, lim Limits) ([]byte, error) {
	zr, closeFn, err := decompress(img.Layer, img.LayerZstd, lim.MaxUnpacked)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	cr := &cappedReader{r: zr, left: lim.MaxUnpacked}
	tr := tar.NewReader(cr)
	want := strings.TrimPrefix(path.Clean("/"+p), "/")
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not in the layer", p)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && strings.TrimPrefix(path.Clean("/"+h.Name), "/") == want {
			return io.ReadAll(io.LimitReader(tr, lim.MaxUnpacked))
		}
	}
}

// Smoke runs img under the engine and, when wt is not nil, under wasmtime, and
// compares them byte for byte.
func Smoke(ctx context.Context, img Image, probes []Probe, engine Executor, wt Executor, lim Limits) SmokeReport {
	// The spec's probes are added to the defaults, never instead of them: the
	// defaults still compare the engine with wasmtime on every image.
	probes = append(append([]Probe(nil), DefaultProbes...), probes...)
	rep := SmokeReport{Image: img.Name, Project: img.Project, Version: img.Version, Verdict: "fail"}
	var module []byte
	if wt != nil {
		var err error
		if module, err = ExtractFile(img, img.ModulePath, lim); err != nil {
			rep.Diffs = append(rep.Diffs, "extract: "+err.Error())
			wt = nil
		}
	}
	anyDefault, specFail := false, false
	for i, pr := range probes {
		args := pr.Args
		if pr.Stdin != "" {
			args = append(append([]string(nil), args...), "<stdin")
		}
		run := Run{Args: pr.Args, Stdin: pr.Stdin, Engine: engine.Run(ctx, img, module, pr)}
		pass := run.Engine.Exit == 0 && run.Engine.Err == "" && (run.Engine.Stdout != "" || run.Engine.Stderr != "" || pr.AllowEmpty)
		if i < len(DefaultProbes) {
			anyDefault = anyDefault || pass
		} else if !pass {
			specFail = true
			rep.Diffs = append(rep.Diffs, fmt.Sprintf("%s: the probe does not pass under the engine (exit %d, err %q)", strings.Join(args, " "), run.Engine.Exit, run.Engine.Err))
		}
		if wt != nil {
			r := wt.Run(ctx, img, module, pr)
			run.Wasmtime = &r
			rep.Diffs = append(rep.Diffs, Compare(args, run.Engine, r)...)
		}
		rep.Runs = append(rep.Runs, run)
	}
	// An image with probes of its own is ok when they all pass (lua answers
	// -v, not --version); one without, when --version or --help does.
	if len(probes) > len(DefaultProbes) {
		if !specFail {
			rep.Verdict = "ok"
		}
	} else if anyDefault {
		rep.Verdict = "ok"
	}
	return rep
}

// Compare lists the differences between two results, byte for byte.
func Compare(args []string, a, b Result) []string {
	var d []string
	tag := strings.Join(args, " ")
	if a.Exit != b.Exit {
		d = append(d, fmt.Sprintf("%s: exit status engine %d, wasmtime %d", tag, a.Exit, b.Exit))
	}
	if a.Stdout != b.Stdout {
		d = append(d, fmt.Sprintf("%s: stdout differs (engine %d bytes, wasmtime %d bytes)", tag, len(a.Stdout), len(b.Stdout)))
	}
	if a.Stderr != b.Stderr {
		d = append(d, fmt.Sprintf("%s: stderr differs (engine %d bytes, wasmtime %d bytes)", tag, len(a.Stderr), len(b.Stderr)))
	}
	return d
}

// Markdown renders the findings and the smoke results as a GitHub step
// summary: one table of images, then the findings grouped by project, ready to
// forward to the bottle's owners.
func Markdown(fs []Finding, reps []SmokeReport, expected map[string]Expectation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### pkgx bottles: %d image(s) smoke-tested\n\n| image | project | version | verdict | differences vs wasmtime | expected |\n|---|---|---|---|---|---|\n", len(reps))
	for _, r := range reps {
		exp := ""
		if e, ok := expected[r.Image]; ok {
			exp = e.Verdict + ": " + e.Reason
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s |\n", r.Image, r.Project, r.Version, r.Verdict, len(r.Diffs), exp)
	}
	for _, r := range reps {
		for _, d := range r.Diffs {
			fmt.Fprintf(&b, "\n- difference `%s`: %s", r.Image, d)
		}
	}
	fmt.Fprintf(&b, "\n\n### Findings for go-pkgx (%d)\n\n", len(fs))
	if len(fs) == 0 {
		b.WriteString("none\n")
	}
	for _, f := range fs {
		fmt.Fprintf(&b, "- `%s` %s: %s\n", f.Project, f.Kind, f.Detail)
	}
	return b.String()
}

// Expectation names a smoke result that is known and explained. An expected
// failure is never skipped silently: it is listed, with its reason, and it
// must still happen (a fixed one that stays listed is stale and fails).
type Expectation struct {
	// Verdict is "fail" (the engine run does not pass) or "diff" (it passes but
	// differs from wasmtime).
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// Judge applies the expectations to the reports and returns the problems:
// an unexpected failure or difference, and a stale expectation.
func Judge(reps []SmokeReport, expected map[string]Expectation) []string {
	var bad []string
	seen := map[string]bool{}
	for _, r := range reps {
		seen[r.Image] = true
		got := "pass"
		switch {
		case r.Verdict != "ok":
			got = "fail"
		case len(r.Diffs) > 0:
			got = "diff"
		}
		exp, listed := expected[r.Image]
		switch {
		case got == "pass" && listed:
			bad = append(bad, fmt.Sprintf("%s: listed as expected %q (%s) but now passes cleanly: remove the entry", r.Image, exp.Verdict, exp.Reason))
		case got != "pass" && !listed:
			bad = append(bad, fmt.Sprintf("%s: unexpected %s %v", r.Image, got, r.Diffs))
		case got != "pass" && listed && exp.Verdict != got:
			bad = append(bad, fmt.Sprintf("%s: expected %q, got %q", r.Image, exp.Verdict, got))
		}
	}
	var gone []string
	for n := range expected {
		if !seen[n] {
			gone = append(gone, n)
		}
	}
	sort.Strings(gone)
	for _, n := range gone {
		bad = append(bad, fmt.Sprintf("%s: listed as expected but no such image was smoke-tested", n))
	}
	return bad
}

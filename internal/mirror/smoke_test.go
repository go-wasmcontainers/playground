package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type fakeExec struct {
	name string
	res  map[string]Result // by joined args
}

func (f fakeExec) Name() string { return f.name }
func (f fakeExec) Run(_ context.Context, _ Image, _ []byte, p Probe) Result {
	return f.res[strings.Join(p.Args, " ")]
}

func TestSmokeAndCompare(t *testing.T) {
	img := Image{Name: "sqlite3", Project: "sqlite.org", Version: "3", Entrypoint: []string{"/bin/sqlite3.wasm"}}
	// ExtractFile needs a real layer; build one.
	layer, err := TarGz(map[string][]byte{"bin/sqlite3.wasm": []byte("\x00asm")}, img.Created)
	if err != nil {
		t.Fatal(err)
	}
	img.Layer = layer
	same := Result{Stdout: "3.53\n"}
	eng := fakeExec{"engine", map[string]Result{"--version": same, "--help": {Stdout: "usage", Exit: 0}}}
	wt := fakeExec{"wasmtime", map[string]Result{"--version": same, "--help": {Stdout: "usage", Exit: 0}}}
	r := Smoke(context.Background(), img, nil, eng, wt, DefaultBottleLimits)
	if r.Verdict != "ok" || len(r.Diffs) != 0 || len(r.Runs) != 2 || r.Runs[0].Wasmtime == nil {
		t.Errorf("identical runs: %+v", r)
	}
	// Planted difference (the sqlite3 tty finding: a box table vs a plain one).
	wt.res["--help"] = Result{Stdout: "usage\n"}
	r = Smoke(context.Background(), img, nil, eng, wt, DefaultBottleLimits)
	if len(r.Diffs) != 1 || !strings.Contains(r.Diffs[0], "stdout differs") {
		t.Errorf("planted diff not seen: %+v", r.Diffs)
	}
	// Exit status and stderr.
	if d := Compare([]string{"x"}, Result{Exit: 0, Stderr: "a"}, Result{Exit: 1, Stderr: "b"}); len(d) != 2 {
		t.Errorf("compare: %v", d)
	}
	// Engine failure: verdict fail.
	r = Smoke(context.Background(), img, nil, fakeExec{"engine", map[string]Result{"--version": {Exit: 1, Stderr: "boom"}}}, nil, DefaultBottleLimits)
	if r.Verdict != "fail" {
		t.Errorf("failing run: %+v", r)
	}
}

func TestSpecFileAndMarkdown(t *testing.T) {
	b, err := os.ReadFile("testdata/smoke.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec Spec
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Probes["lua"]) == 0 {
		t.Errorf("no lua probe: %+v", spec)
	}
	md := Markdown([]Finding{{Project: "p", Kind: "k", Detail: "d"}}, []SmokeReport{{Image: "i", Project: "p", Version: "1", Verdict: "ok", Diffs: []string{"x"}}}, map[string]Expectation{"i": {"diff", "why"}})
	for _, want := range []string{"| i | p | 1 | ok | 1 | diff: why |", "difference `i`: x", "`p` k: d"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestJudge(t *testing.T) {
	reps := []SmokeReport{
		{Image: "good", Verdict: "ok"},
		{Image: "known-fail", Verdict: "fail"},
		{Image: "known-diff", Verdict: "ok", Diffs: []string{"d"}},
		{Image: "surprise", Verdict: "fail"},
		{Image: "fixed", Verdict: "ok"},
		{Image: "wrong-kind", Verdict: "fail"},
	}
	exp := map[string]Expectation{
		"known-fail": {"fail", "r"}, "known-diff": {"diff", "r"}, "fixed": {"fail", "r"},
		"wrong-kind": {"diff", "r"}, "vanished": {"fail", "r"},
	}
	bad := strings.Join(Judge(reps, exp), "\n")
	for _, want := range []string{"surprise: unexpected fail", "fixed: listed as expected", "wrong-kind: expected", "vanished: listed as expected but no such image"} {
		if !strings.Contains(bad, want) {
			t.Errorf("missing %q in\n%s", want, bad)
		}
	}
	if strings.Contains(bad, "known-fail") || strings.Contains(bad, "known-diff") || strings.Contains(bad, "good") {
		t.Errorf("expected results flagged:\n%s", bad)
	}
}

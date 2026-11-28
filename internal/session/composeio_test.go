package session

import (
	"context"
	"strings"
	"testing"

	"github.com/go-wasmcontainers/playground/internal/composefs"
)

type rec struct {
	imported []string
	failed   []string
	doneArgs [][]string
	doneOut  []string
}

func (r *rec) report() ComposeReport {
	return ComposeReport{
		Imported: func(p string) { r.imported = append(r.imported, p) },
		Failed:   func(m string) { r.failed = append(r.failed, m) },
		Done:     func(a []string, o string) { r.doneArgs = append(r.doneArgs, a); r.doneOut = append(r.doneOut, o) },
	}
}

const demoFile = "name: demo\nservices:\n  web:\n    image: hello\n"

func demoFiles() []composefs.File {
	return []composefs.File{{Name: "compose.yaml", Data: []byte(demoFile)}}
}

// P1/P2: an import names its project, runs the preview and reports it as run.
func TestImportComposeReportsProjectAndPreview(t *testing.T) {
	var r rec
	s := noEngine()
	if msg := s.ImportCompose(context.Background(), demoFiles(), r.report()); msg != "" {
		t.Fatalf("refused: %s", msg)
	}
	if len(r.imported) != 1 || r.imported[0] != "demo" {
		t.Fatalf("imported = %q", r.imported)
	}
	if len(r.doneArgs) != 1 || strings.Join(r.doneArgs[0], " ") != "compose config" || !strings.Contains(r.doneOut[0], "web:") {
		t.Fatalf("done = %q / %q", r.doneArgs, r.doneOut)
	}
	if len(r.failed) != 0 {
		t.Fatalf("failed = %q", r.failed)
	}
}

// A refusal is visible and nothing is claimed to have run.
func TestImportComposeRefusalIsReported(t *testing.T) {
	var r rec
	s := noEngine()
	msg := s.ImportCompose(context.Background(), []composefs.File{{Name: "../x"}}, r.report())
	if msg == "" || len(r.failed) != 1 || r.failed[0] != msg {
		t.Fatalf("msg %q failed %q", msg, r.failed)
	}
	if len(r.doneArgs) != 0 || len(r.imported) != 0 {
		t.Fatalf("a refused import ran a command or named a project: %q %q", r.doneArgs, r.imported)
	}
}

// Files that are not a valid project are kept out of the export: no project,
// a visible error, and no command shown as run.
func TestImportComposeInvalidProject(t *testing.T) {
	var r rec
	s := noEngine()
	msg := s.ImportCompose(context.Background(), []composefs.File{{Name: "compose.yaml", Data: []byte("services: [")}}, r.report())
	if msg == "" || len(r.failed) == 0 {
		t.Fatalf("an invalid file was accepted: %q %q", msg, r.failed)
	}
	if len(r.doneArgs) != 0 {
		t.Fatalf("a failed preview was reported as run: %q", r.doneArgs)
	}
	if len(r.imported) != 1 || r.imported[0] != "" {
		t.Fatalf("imported = %q, want one empty project", r.imported)
	}
}

func TestExportComposeServesTheImportedProjectOnly(t *testing.T) {
	var r rec
	s := noEngine()
	if msg := s.ImportCompose(context.Background(), demoFiles(), r.report()); msg != "" {
		t.Fatal(msg)
	}
	r = rec{}
	text, err := s.ExportCompose(context.Background(), "demo", r.report())
	if err != nil || !strings.Contains(text, "web:") {
		t.Fatalf("export: %v %q", err, text)
	}
	if len(r.doneArgs) != 1 || strings.Join(r.doneArgs[0], " ") != "compose -p demo config" {
		t.Fatalf("done = %q", r.doneArgs)
	}
	r = rec{}
	if _, err := s.ExportCompose(context.Background(), "other", r.report()); err == nil || len(r.failed) != 1 || len(r.doneArgs) != 0 {
		t.Fatalf("another project exported: %v %q %q", err, r.failed, r.doneArgs)
	}
	if _, err := s.ExportCompose(context.Background(), "a b; rm", r.report()); err == nil {
		t.Fatal("a hostile project name was accepted")
	}
}

// D3: if the store lost the files the export is refused, visibly.
func TestExportComposeStoreLostFiles(t *testing.T) {
	var r rec
	s := noEngine()
	if msg := s.ImportCompose(context.Background(), demoFiles(), r.report()); msg != "" {
		t.Fatal(msg)
	}
	s.ComposeFiles().Clear()
	r = rec{}
	_, err := s.ExportCompose(context.Background(), "demo", r.report())
	if err == nil || len(r.failed) != 1 || !strings.Contains(r.failed[0], "import") || len(r.doneArgs) != 0 {
		t.Fatalf("err %v failed %q done %q", err, r.failed, r.doneArgs)
	}
}

// P3: HasCompose tells the page a drop would replace something.
func TestHasCompose(t *testing.T) {
	var r rec
	s := noEngine()
	if s.HasCompose() {
		t.Fatal("a new session has compose files")
	}
	s.ImportCompose(context.Background(), demoFiles(), r.report())
	if !s.HasCompose() {
		t.Fatal("an imported session has none")
	}
}

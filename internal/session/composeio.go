package session

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"

	"github.com/go-wasmcontainers/playground/internal/composefs"
)

// projectName is what a compose project may be called when the page asks for
// its export: the name reaches a shell line, so nothing else gets through.
var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ComposeReport is where the outcome of a compose import or export goes (the
// dashboard's Topology viewmodel, on the page). Every field may be nil.
type ComposeReport struct {
	// Imported names the project the current import loaded ("" when the files
	// name none or do not load).
	Imported func(project string)
	// Failed shows a refusal or an error.
	Failed func(msg string)
	// Done reports a wctr command that really ran, with its output.
	Done func(args []string, output string)
}

func (r ComposeReport) imported(p string) {
	if r.Imported != nil {
		r.Imported(p)
	}
}

func (r ComposeReport) failed(m string) {
	if r.Failed != nil {
		r.Failed(m)
	}
}

func (r ComposeReport) done(a []string, o string) {
	if r.Done != nil {
		r.Done(a, o)
	}
}

// importedProject is the project of the last successful import, which is the
// only one ExportCompose serves.
type importedProject struct {
	mu   sync.Mutex
	name string
}

func (p *importedProject) set(n string) { p.mu.Lock(); p.name = n; p.mu.Unlock() }
func (p *importedProject) get() string  { p.mu.Lock(); defer p.mu.Unlock(); return p.name }

// HasCompose reports whether the session holds imported compose files: a new
// import would replace them.
func (s *Session) HasCompose() bool { return len(s.ComposeFiles().Names()) > 0 }

// ImportCompose keeps files as the session's compose set, runs
// `wctr compose config` over them as a preview, and reports the project and
// the preview. It returns "" or the refusal (also reported through Failed).
func (s *Session) ImportCompose(ctx context.Context, files []composefs.File, rep ComposeReport) string {
	if err := s.ComposeFiles().Import(files); err != nil {
		s.imp.set("")
		rep.failed(err.Error())
		return err.Error()
	}
	var out bytes.Buffer
	r := s.Run(ctx, "wctr compose config", &out)
	if r.Code != 0 {
		s.imp.set("")
		rep.imported("")
		msg := "import: compose config: " + strings.TrimSpace(out.String())
		rep.failed(msg)
		return msg
	}
	project := projectOf(out.String())
	s.imp.set(project)
	rep.imported(project)
	rep.done([]string{"compose", "config"}, out.String())
	return ""
}

// ExportCompose returns the resolved compose text of the imported project. It
// is refused, and the refusal reported, for any other project, for a name that
// is not one, and when the store no longer holds the imported files.
func (s *Session) ExportCompose(ctx context.Context, project string, rep ComposeReport) (string, error) {
	fail := func(msg string) (string, error) {
		rep.failed(msg)
		return "", errors.New(msg)
	}
	if !projectName.MatchString(project) {
		return fail("export: not a project name")
	}
	if !s.HasCompose() {
		return fail("export: the imported files are no longer held; import them again")
	}
	if imp := s.imp.get(); imp == "" || imp != project {
		return fail("export: only the imported project can be exported")
	}
	args := []string{"compose", "-p", project, "config"}
	var out bytes.Buffer
	r := s.Run(ctx, "wctr "+strings.Join(args, " "), &out)
	if r.Code != 0 || out.Len() == 0 {
		return fail("export: " + strings.TrimSpace(out.String()))
	}
	rep.done(args, out.String())
	return out.String(), nil
}

// projectOf reads the top-level `name:` of a resolved compose document.
func projectOf(config string) string {
	for _, l := range strings.Split(config, "\n") {
		if v, ok := strings.CutPrefix(l, "name:"); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if projectName.MatchString(v) {
				return v
			}
			return ""
		}
	}
	return ""
}

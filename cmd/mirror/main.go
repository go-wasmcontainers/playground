// Command mirror inspects and smoke-tests the pkgx bottles the playground's
// static image registry carries. The bottle list is not typed anywhere: it is
// derived from pkgx's signed wasip1/wasm catalogue.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-wasmcontainers/playground/internal/mirror"
)

var osExit = os.Exit

func main() {
	cmd := RootCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "mirror:", err)
		osExit(1)
	}
}

// RootCmd builds the command tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{Use: "mirror", Short: "Inspect and smoke-test the pkgx bottles the playground mirrors", SilenceUsage: true, SilenceErrors: true}

	var untilImages int
	var every, waitMax time.Duration
	inspect := &cobra.Command{
		Use:   "inspect",
		Short: "Derive the images from the signed catalogue and list what each bottle holds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			deadline := time.Now().Add(waitMax)
			var der *mirror.Derived
			for {
				var err error
				der, err = mirror.Bottles(cmd.Context(), mirror.DefaultBottleLimits, nil, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if len(der.Images) >= untilImages {
					break
				}
				if time.Now().Add(every).After(deadline) {
					return fmt.Errorf("%d image(s) after %s, wanted %d", len(der.Images), waitMax, untilImages)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "mirror: %d image(s), waiting for %d\n", len(der.Images), untilImages)
				select {
				case <-time.After(every):
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				}
			}
			for _, im := range der.Images {
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s %s:%s  %d bytes  %s\n", im.Name, im.Project, im.Version, len(im.Layer), im.ModulePath)
			}
			for _, f := range der.Findings {
				fmt.Fprintf(cmd.OutOrStdout(), "finding %-30s %-22s %s\n", f.Project, f.Kind, f.Detail)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d image(s), %d finding(s), %d bytes of layers (%d MiB, each image counted)\n", len(der.Images), len(der.Findings), der.LayerBytes, der.LayerBytes>>20)
			return nil
		},
	}
	inspect.Flags().IntVar(&untilImages, "until-images", 0, "keep re-deriving until at least this many images (waits for a catalogue run)")
	inspect.Flags().DurationVar(&every, "every", 2*time.Minute, "pause between derivations with --until-images")
	inspect.Flags().DurationVar(&waitMax, "wait", 30*time.Minute, "give up after this long with --until-images")
	root.AddCommand(inspect)

	var (
		out, specFile, only, summary string
		requireWasmtime              bool
		engineWASI                   bool
	)
	smoke := &cobra.Command{
		Use:   "smoke",
		Short: "Run every derived image with --version and --help under the engine (wazero) and wasmtime, and compare",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var spec mirror.Spec
			if specFile != "" {
				b, err := os.ReadFile(specFile)
				if err != nil {
					return err
				}
				dec := json.NewDecoder(strings.NewReader(string(b)))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&spec); err != nil {
					return fmt.Errorf("%s: %w", specFile, err)
				}
			}
			expected := spec.Expected
			if expected == nil {
				expected = map[string]mirror.Expectation{}
			}
			var wt mirror.Executor
			if p, err := exec.LookPath("wasmtime"); err == nil {
				wt = mirror.Wasmtime{Bin: p}
			} else if requireWasmtime {
				return fmt.Errorf("wasmtime is required (--require-wasmtime) and is not on PATH")
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(), "mirror: wasmtime is not on PATH: engine-only run, nothing is compared")
			}
			der, err := mirror.Bottles(cmd.Context(), mirror.DefaultBottleLimits, nil, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			var reps []mirror.SmokeReport
			for _, im := range der.Images {
				if only != "" && !strings.Contains(","+only+",", ","+im.Name+",") {
					continue
				}
				r := mirror.Smoke(cmd.Context(), im, spec.Probes[im.Name], mirror.Native{EngineWASI: engineWASI}, wt, mirror.DefaultBottleLimits)
				reps = append(reps, r)
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s %-5s %d difference(s)\n", r.Image, r.Verdict, len(r.Diffs))
			}
			if only != "" {
				keep := map[string]mirror.Expectation{}
				for _, r := range reps {
					if e, ok := expected[r.Image]; ok {
						keep[r.Image] = e
					}
				}
				expected = keep
			}
			if out != "" {
				b, err := json.MarshalIndent(struct {
					Findings []mirror.Finding     `json:"findings"`
					Smoke    []mirror.SmokeReport `json:"smoke"`
				}{der.Findings, reps}, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
					return err
				}
			}
			if summary != "" {
				if err := os.WriteFile(summary, []byte(mirror.Markdown(der.Findings, reps, expected)), 0o644); err != nil {
					return err
				}
			}
			bad := mirror.Judge(reps, expected)
			for _, b := range bad {
				fmt.Fprintln(cmd.ErrOrStderr(), "smoke:", b)
			}
			if len(bad) > 0 {
				return fmt.Errorf("%d unexpected smoke result(s)", len(bad))
			}
			return nil
		},
	}
	smoke.Flags().StringVar(&out, "out", "", "write the full report (stdout, stderr, exit status of every run) to this JSON file")
	smoke.Flags().StringVar(&summary, "summary", "", "write a Markdown summary (a GitHub step summary) to this file")
	smoke.Flags().StringVar(&specFile, "spec", "", "JSON file: per-image probes, and the expected failures and differences, each with its reason")
	smoke.Flags().StringVar(&only, "only", "", "comma-separated image names to test")
	smoke.Flags().BoolVar(&requireWasmtime, "require-wasmtime", false, "fail when wasmtime is not on PATH instead of running the engine alone")
	smoke.Flags().BoolVar(&engineWASI, "engine-wasi", true, "use the engine's own WASI host (what the page runs), not wazero's")
	root.AddCommand(smoke)
	return root
}

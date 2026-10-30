// Command site builds, serves and verifies the playground's static site.
//
//	site build  --out _site [--pkgx] [--version v0.x.y]
//	site serve  --dir _site [--addr 127.0.0.1:8080] [--isolate]
//	site verify --url https://go-wasmcontainers.github.io/playground/ --manifest _site/build.json
//	site thirdparty --dir web/vendor
//	site usercontent --allow https://go-wasmcontainers.github.io --out _site
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-wasmcontainers/playground/internal/mirror"
	"github.com/go-wasmcontainers/playground/internal/site"
	"github.com/go-wasmcontainers/playground/internal/thirdparty"
	"github.com/go-wasmcontainers/playground/internal/usercontent"
)

var osExit = os.Exit

func main() {
	cmd := RootCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "site:", err)
		osExit(1)
	}
}

// RootCmd builds the command tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{Use: "site", Short: "Build, serve and verify the playground site", SilenceUsage: true, SilenceErrors: true}

	var o site.Options
	build := &cobra.Command{
		Use:   "build",
		Short: "Assemble the site: images and their /v2 mirror, the engine, the page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Root == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				o.Root = wd
			}
			o.Log = cmd.OutOrStdout()
			_, err := site.Build(cmd.Context(), o)
			return err
		},
	}
	build.Flags().StringVar(&o.Out, "out", "_site", "output directory (replaced)")
	build.Flags().StringVar(&o.Root, "root", "", "playground module root (default: the working directory)")
	build.Flags().BoolVar(&o.Pkgx, "pkgx", false, "add the pkgx wasip1 pilot bottles from ghcr.io")
	build.Flags().StringVar(&o.Version, "version", "dev", "engine version shown by wctr version")
	build.Flags().BoolVar(&o.Remote, "remote", false, "add the images pinned by digest (the shell) from ghcr.io")

	pin := &cobra.Command{
		Use:   "pin REFERENCE",
		Short: "Print the digest a registry serves for a reference, to pin it in internal/mirror",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := mirror.Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), d)
			return nil
		},
	}

	var dir, addr, prefix string
	var isolate bool
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a built site locally under /playground/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "http://%s%s/\n", l.Addr(), prefix)
			srv := &http.Server{Handler: site.Handler(dir, prefix, isolate), ReadHeaderTimeout: 10 * time.Second}
			return srv.Serve(l)
		},
	}
	serve.Flags().StringVar(&dir, "dir", "_site", "site directory")
	serve.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "listen address (loopback)")
	serve.Flags().StringVar(&prefix, "prefix", "/playground", "path prefix, as on GitHub Pages")
	serve.Flags().BoolVar(&isolate, "isolate", false, "send COOP/COEP/CSP headers instead of relying on the service worker")

	var url, manifest string
	var timeout, every time.Duration
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Wait until a deployed site serves exactly the files of a build",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(manifest)
			if err != nil {
				return err
			}
			var m site.Manifest
			if err := json.Unmarshal(b, &m); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			took, err := site.Verify(ctx, url, &m, every, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "live: build %s, %d files byte-identical, after %s\n", m.Build, len(m.Files), took.Round(time.Second))
			return nil
		},
	}
	verify.Flags().StringVar(&url, "url", "https://go-wasmcontainers.github.io/playground/", "deployed site")
	verify.Flags().StringVar(&manifest, "manifest", "_site/build.json", "the build's manifest")
	verify.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "how long to wait for the CDN")
	verify.Flags().DurationVar(&every, "every", 30*time.Second, "retry interval")

	var vdir string
	tp := &cobra.Command{
		Use:   "thirdparty",
		Short: "Fetch the pinned xterm.js files from npm, integrity-checked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return thirdparty.Fetch(cmd.Context(), vdir)
		},
	}
	tp.Flags().StringVar(&vdir, "dir", "web/vendor", "output directory")

	var ddir, dver string
	drv := &cobra.Command{
		Use:   "playwright-driver",
		Short: "Assemble the Playwright driver from npm (playwright-core), integrity-checked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return thirdparty.PlaywrightDriver(cmd.Context(), ddir, dver)
		},
	}
	drv.Flags().StringVar(&ddir, "dir", "", "driver directory (PLAYWRIGHT_DRIVER_PATH)")
	drv.Flags().StringVar(&dver, "version", "1.62.1", "playwright-core version (must match playwright-go v0.6201.1)")
	drv.MarkFlagRequired("dir")

	var allow, ucOut string
	var local bool
	uc := &cobra.Command{
		Use:   "usercontent",
		Short: "Write the site of a usercontent origin: the shell that shows a container's port inside the playground at --allow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := usercontent.Render(allow, ucOut, local); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d files, for %s\n", ucOut, len(usercontent.Files), allow)
			return nil
		},
	}
	uc.Flags().StringVar(&allow, "allow", "", "the playground's origin, the only page the shell talks to (https://host)")
	uc.Flags().StringVar(&ucOut, "out", "_site", "output directory")
	uc.Flags().BoolVar(&local, "local", false, "also accept a loopback http origin (tests)")
	uc.MarkFlagRequired("allow")

	root.AddCommand(build, pin, serve, verify, tp, drv, uc)
	return root
}

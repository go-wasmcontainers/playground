// Command mirror builds the playground's static image registry (a /v2 tree
// served by GitHub Pages) and inspects the pkgx bottles it carries.
package main

import (
	"fmt"
	"os"

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
	root := &cobra.Command{Use: "mirror", Short: "Build and inspect the playground's static image registry", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(&cobra.Command{
		Use:   "inspect",
		Short: "Fetch the pkgx pilot bottles and list what they hold",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, b := range mirror.Pilots {
				bl, err := mirror.FetchBottle(cmd.Context(), b, 100<<20)
				if bl != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s (%s): layer %s, %d bytes, wasm %q\n", b.Name, b.Ref, bl.MediaType, len(bl.Layer), bl.WasmPath)
					for _, e := range bl.Entries {
						fmt.Fprintln(cmd.OutOrStdout(), "   ", e)
					}
				}
				if err != nil {
					return err
				}
			}
			return nil
		},
	})
	return root
}

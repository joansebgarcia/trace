// Package cmd defines the command-line interface, one file per command.
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/joansebgarcia/amber/web"
)

// binary is the product name, used for the binary and the env prefix. It is
// the only place the name appears, so renaming the product is one edit.
const binary = "amber"

// EnvPrefix starts the name of the env variable behind every flag.
var EnvPrefix = strings.ToUpper(binary) + "_"

// NewRoot returns the root command, reporting version on --version along
// with whether the binary embeds the web UI.
//
// Every flag of every command can also be set with an env variable named
// after it (--log-level → AMBER_LOG_LEVEL). A flag on the command line wins
// over its env variable, which wins over the default. An empty env variable
// counts as unset.
func NewRoot(version string) *cobra.Command {
	// Run the env hook below even when a subcommand adds its own
	// PersistentPreRunE; by default cobra runs only the nearest one.
	cobra.EnableTraverseRunHooks = true

	root := &cobra.Command{
		Use:           binary,
		Short:         "Self-hosted engine for capturing, grouping and diagnosing application errors",
		Version:       formatVersion(version),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return applyEnv(cmd.Flags())
		},
	}

	root.AddCommand(newServe())

	for _, cmd := range root.Commands() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			f.Usage += fmt.Sprintf(" [$%s]", formatEnvName(f))
		})
	}

	return root
}

func formatVersion(version string) string {
	if web.Embedded {
		return version + " (with the web UI embedded)"
	}
	return version + " (without the web UI)"
}

func applyEnv(flags *pflag.FlagSet) error {
	var err error
	flags.VisitAll(func(f *pflag.Flag) {
		if err != nil || f.Changed || f.Name == "help" || f.Name == "version" {
			return
		}
		value, ok := lookupEnv(formatEnvName(f))
		if !ok {
			return
		}
		if setErr := f.Value.Set(value); setErr != nil {
			err = fmt.Errorf("invalid %s %q: %w", formatEnvName(f), value, setErr)
		}
	})
	return err
}

func formatEnvName(f *pflag.Flag) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
}

func lookupEnv(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	return value, ok && value != ""
}

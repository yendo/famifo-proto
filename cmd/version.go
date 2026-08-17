package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func NewCmdVersion(out io.Writer) *cobra.Command {
	// versionCmd represents the version command
	cmd := &cobra.Command{
		Use:   "version",
		Short: "A brief description of your command",
		Long:  `A longer description.`,
		// Run: func(cmd *cobra.Command, args []string) {
		// 	fmt.Println("version called")
		// },
		Run: run,
	}

	cmd.SetOutput(out)

	return cmd
}

func run(cmd *cobra.Command, args []string) {
	out := cmd.OutOrStdout()
	fmt.Fprint(out, "version called!")
}

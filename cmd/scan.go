package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func NewCmdScan(out io.Writer) *cobra.Command {
	// versionCmd represents the version command
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "A brief description of your command",
		Long:  `A longer description.`,
		// Run: func(cmd *cobra.Command, args []string) {
		// 	fmt.Println("version called")
		// },
		Run: runScan,
	}

	cmd.SetOutput(out)

	return cmd
}

func runScan(cmd *cobra.Command, args []string) {
	out := cmd.OutOrStdout()
	fmt.Fprint(out, "scan!")
}

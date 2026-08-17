package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"
)

func NewCmdRoot(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "famifo-proto",
		Short: "A brief description of your application",
		Long:  `A longer description.`,
		// Run: func(cmd *cobra.Command, args []string) { },
	}

	cmd.SetOutput(out)
	cmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

	cmd.AddCommand(NewCmdVersion(out))
	cmd.AddCommand(NewCmdScan(out))

	return cmd
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	cmd := NewCmdRoot(os.Stdout)
	err := cmd.Execute()

	if err != nil {
		os.Exit(1)
	}
}

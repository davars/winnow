package cmd

import (
	"github.com/spf13/cobra"
)

var cfgFile string
var dataDir string

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "winnow",
		Short:         "File organization tool for cleaning up old backups",
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory (overrides config lookup)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newImportConfigCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newWalkCmd())
	root.AddCommand(newReconcileCmd())
	root.AddCommand(newSHA256Cmd())
	root.AddCommand(newMimeCmd())
	root.AddCommand(newExifCmd())
	root.AddCommand(newQueryCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newProcessCmd())
	root.AddCommand(newOrganizeCmd())
	root.AddCommand(newExecCmd())

	return root
}

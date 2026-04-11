package cmd

import (
	"github.com/spf13/cobra"
)

var cfgFile string

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "winnow",
		Short: "File organization tool for cleaning up old backups",
	}

	root.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")

	root.AddCommand(newInitCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newWalkCmd())
	root.AddCommand(newReconcileCmd())
	root.AddCommand(newSHA256Cmd())
	root.AddCommand(newMimeCmd())
	root.AddCommand(newExifCmd())

	return root
}

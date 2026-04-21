package cmd

import (
	"fmt"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/spf13/cobra"
)

func newImportConfigCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "import-config",
		Short: "Temporarily import a legacy full TOML config into the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImportConfig(force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "replace an existing settings row")

	return cmd
}

func runImportConfig(force bool) error {
	if dataDir != "" {
		return fmt.Errorf("--data-dir is not supported by import-config; use --config to point at the legacy TOML")
	}

	path, err := config.Find(cfgFile)
	if err != nil {
		return err
	}

	legacy, err := config.LoadLegacy(path)
	if err != nil {
		return err
	}

	database, err := db.Open(legacy.BootstrapConfig().DBPath())
	if err != nil {
		return err
	}
	defer database.Close()

	hasSettings, err := db.HasSettings(database)
	if err != nil {
		return err
	}
	if hasSettings && !force {
		return fmt.Errorf("settings already exist in %s — rerun with --force to replace them", legacy.BootstrapConfig().DBPath())
	}

	if err := db.SaveSettings(database, legacy.RuntimeSettings()); err != nil {
		return err
	}
	if err := config.Save(path, legacy.BootstrapConfig()); err != nil {
		return err
	}

	fmt.Printf("Imported settings from %s into %s\n", path, legacy.BootstrapConfig().DBPath())
	return nil
}

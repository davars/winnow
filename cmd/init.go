package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/davars/winnow/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive setup: prompts for paths, writes config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing config file")

	return cmd
}

func runInit(force bool) error {
	dest := config.DefaultConfigPath()

	if !force {
		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("config file already exists at %s (use --force to overwrite)", dest)
		}
	}

	reader := bufio.NewReader(os.Stdin)

	rawDir, err := promptPath(reader, "Raw directory (unsorted files)", "")
	if err != nil {
		return err
	}
	cleanDir, err := promptPath(reader, "Clean directory (organized files)", "")
	if err != nil {
		return err
	}
	trashDir, err := promptPath(reader, "Trash directory (files staged for deletion)", "")
	if err != nil {
		return err
	}
	dataDir, err := promptPath(reader, "Data directory (winnow database)", "")
	if err != nil {
		return err
	}
	tz, err := pickTimezone(reader, os.Stdout)
	if err != nil {
		return err
	}

	cfg := config.Config{
		RawDir:   rawDir,
		CleanDir: cleanDir,
		TrashDir: trashDir,
		DataDir:  dataDir,
		Organize: config.OrganizeConfig{Timezone: tz},
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := config.Save(dest, &cfg); err != nil {
		return err
	}

	fmt.Printf("Config written to %s\n", dest)
	return nil
}

func promptPath(reader *bufio.Reader, label, defaultVal string) (string, error) {
	for {
		if defaultVal != "" {
			fmt.Printf("%s [%s]: ", label, defaultVal)
		} else {
			fmt.Printf("%s: ", label)
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)

		if line == "" && defaultVal != "" {
			line = defaultVal
		}
		if line == "" {
			fmt.Println("  Path is required.")
			continue
		}

		// Expand ~ to home directory.
		if strings.HasPrefix(line, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("expanding ~: %w", err)
			}
			line = filepath.Join(home, line[2:])
		}

		abs, err := filepath.Abs(line)
		if err != nil {
			return "", fmt.Errorf("resolving path: %w", err)
		}

		if _, err := os.Stat(abs); os.IsNotExist(err) {
			fmt.Printf("  %s does not exist. Create it? [Y/n]: ", abs)
			ans, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			ans = strings.TrimSpace(strings.ToLower(ans))
			if ans == "" || ans == "y" || ans == "yes" {
				if err := os.MkdirAll(abs, 0o755); err != nil {
					return "", fmt.Errorf("creating directory: %w", err)
				}
			} else {
				continue
			}
		}

		return abs, nil
	}
}

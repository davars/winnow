package cmd

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive setup: configure the data dir and database-backed settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit()
		},
	}
	return cmd
}

func runInit() error {
	return runInitWithIO(os.Stdin, os.Stdout)
}

func runInitWithIO(in io.Reader, out io.Writer) error {
	dest, bootstrap, editingExisting, err := resolveInitTarget(dataDir, cfgFile)
	if err != nil {
		return err
	}
	if editingExisting {
		fmt.Fprintf(out, "Editing config at %s\n", dest)
	}

	prompter := newLinePrompter(in, out)

	dataDirValue, err := prompter.promptRequired("Data directory", bootstrap.DataDir, "")
	if err != nil {
		return err
	}
	bootstrap.DataDir, err = expandAndResolve(dataDirValue)
	if err != nil {
		return err
	}
	if err := ensureDir(prompter, bootstrap.DataDir); err != nil {
		return err
	}

	database, err := db.Open(bootstrap.DBPath())
	if err != nil {
		return err
	}
	defer database.Close()

	settings, err := initSettings(database)
	if err != nil {
		return err
	}

	zones, _ := loadZoneList()

	settings.RawDir, err = prompter.promptRequired("Raw directory", settings.RawDir, "")
	if err != nil {
		return err
	}
	settings.CleanDir, err = prompter.promptRequired("Clean directory", settings.CleanDir, "")
	if err != nil {
		return err
	}
	settings.TrashDir, err = prompter.promptRequired("Trash directory", settings.TrashDir, "")
	if err != nil {
		return err
	}
	settings.Organize.Timezone, err = promptTimezone(prompter, settings.Organize.Timezone, zones)
	if err != nil {
		return err
	}
	settings.PreProcessHook, err = prompter.promptOptional(
		"Pre-process hook",
		settings.PreProcessHook,
		`Command run before processing (enter "-" to clear)`,
		true,
	)
	if err != nil {
		return err
	}
	settings.Reconcile.MaxStaleness, err = promptDuration(
		prompter,
		"Reconcile max staleness",
		settings.Reconcile.MaxStaleness,
		`Duration before marking unseen files as missing (enter "-" to clear)`,
	)
	if err != nil {
		return err
	}

	dirs := []*string{&settings.RawDir, &settings.CleanDir, &settings.TrashDir}
	for _, dir := range dirs {
		*dir, err = expandAndResolve(*dir)
		if err != nil {
			return err
		}
	}
	for _, dir := range dirs {
		if err := ensureDir(prompter, *dir); err != nil {
			return err
		}
	}

	if err := settings.Validate(); err != nil {
		return err
	}
	if err := db.SaveSettings(database, settings); err != nil {
		return err
	}
	if err := config.Save(dest, bootstrap); err != nil {
		return err
	}

	fmt.Fprintf(out, "Config written to %s\n", dest)
	fmt.Fprintf(out, "Settings saved in %s\n", bootstrap.DBPath())
	return nil
}

func initSettings(database *sql.DB) (*config.Settings, error) {
	settings, err := db.LoadSettings(database)
	if err == nil {
		return settings, nil
	}
	if !errors.Is(err, db.ErrSettingsNotConfigured) {
		return nil, err
	}

	settings = config.DefaultSettings()
	if tz := detectSystemTZ(); tz != "" {
		settings.Organize.Timezone = tz
	} else {
		settings.Organize.Timezone = "UTC"
	}
	return settings, nil
}

func resolveInitTarget(dataDirFlag, cfgFlag string) (string, *config.Bootstrap, bool, error) {
	current := &config.Bootstrap{}
	if dataDirFlag != "" {
		current.DataDir = dataDirFlag
	}

	if cfgFlag != "" {
		if _, err := os.Stat(cfgFlag); err == nil {
			loaded, err := config.Load(cfgFlag)
			if err != nil {
				return "", nil, false, err
			}
			if current.DataDir == "" {
				current.DataDir = loaded.DataDir
			}
			return cfgFlag, current, true, nil
		}
		return cfgFlag, current, false, nil
	}

	path, err := config.Find("")
	if err != nil {
		return config.DefaultConfigPath(), current, false, nil
	}

	loaded, err := config.Load(path)
	if err != nil {
		return "", nil, false, err
	}
	if current.DataDir == "" {
		current.DataDir = loaded.DataDir
	}
	return path, current, true, nil
}

type linePrompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newLinePrompter(in io.Reader, out io.Writer) *linePrompter {
	return &linePrompter{
		in:  bufio.NewReader(in),
		out: out,
	}
}

func (p *linePrompter) promptRequired(label, current, description string) (string, error) {
	for {
		value, err := p.promptOptional(label, current, description, false)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintf(p.out, "%s is required.\n", label)
	}
}

func (p *linePrompter) promptOptional(label, current, description string, clearable bool) (string, error) {
	if description != "" {
		fmt.Fprintf(p.out, "%s\n", description)
	}
	prompt := label + ": "
	if current != "" {
		prompt = fmt.Sprintf("%s [%s]: ", label, current)
	}
	fmt.Fprint(p.out, prompt)

	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	if line == "" {
		return current, nil
	}
	if clearable && line == "-" {
		return "", nil
	}
	return line, nil
}

func (p *linePrompter) confirm(question string) (bool, error) {
	for {
		fmt.Fprintf(p.out, "%s [y/N]: ", question)
		line, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(line) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.out, `Please answer "y" or "n".`)
		}
	}
}

func (p *linePrompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	return line, nil
}

func promptTimezone(prompter *linePrompter, current string, zones []string) (string, error) {
	for {
		value, err := prompter.promptOptional(
			"Timezone",
			current,
			"IANA timezone for interpreting EXIF timestamps (enter - to clear)",
			true,
		)
		if err != nil {
			return "", err
		}
		if value == "" {
			return "", nil
		}
		if value == "Local" {
			fmt.Fprintf(prompter.out, "Explicit IANA timezone required (not %q).\n", value)
			continue
		}
		if _, err := time.LoadLocation(value); err == nil {
			return value, nil
		}

		matches := matchZones(value, zones)
		if len(matches) > 10 {
			matches = matches[:10]
		}
		if len(matches) == 0 {
			fmt.Fprintf(prompter.out, "Unknown timezone %q.\n", value)
			continue
		}
		fmt.Fprintf(prompter.out, "Unknown timezone %q. Did you mean:\n", value)
		for _, match := range matches {
			fmt.Fprintf(prompter.out, "  %s\n", match)
		}
	}
}

func promptDuration(prompter *linePrompter, label, current, description string) (string, error) {
	for {
		value, err := prompter.promptOptional(label, current, description, true)
		if err != nil {
			return "", err
		}
		if value == "" {
			return "", nil
		}
		if _, err := time.ParseDuration(value); err == nil {
			return value, nil
		}
		fmt.Fprintf(prompter.out, "Invalid duration %q.\n", value)
	}
}

func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func expandAndResolve(path string) (string, error) {
	path = expandTilde(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", path, err)
	}
	return abs, nil
}

func ensureDir(prompter *linePrompter, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	create, err := prompter.confirm(fmt.Sprintf("%s does not exist. Create it?", path))
	if err != nil {
		return err
	}
	if !create {
		return fmt.Errorf("directory %s does not exist", path)
	}
	return os.MkdirAll(path, 0o755)
}

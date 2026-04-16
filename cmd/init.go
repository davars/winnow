package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	huh "charm.land/huh/v2"
	"github.com/davars/winnow/config"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive setup: configure all settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit()
		},
	}
	return cmd
}

func runInit() error {
	dest, current, err := resolveInitTarget(cfgFile)
	if err != nil {
		return err
	}

	zones, _ := loadZoneList()

	form := huh.NewForm(
		huh.NewGroup(
			dirInput("Raw directory", &current.RawDir),
			dirInput("Clean directory", &current.CleanDir),
			dirInput("Trash directory", &current.TrashDir),
			dirInput("Data directory", &current.DataDir),
		).Title("Directories"),
		huh.NewGroup(
			tzInput(&current.Organize.Timezone, zones),
		).Title("Timezone"),
		huh.NewGroup(
			huh.NewInput().
				Title("Pre-process hook").
				Description(`Command run before processing (enter "-" to clear)`).
				Value(&current.PreProcessHook),
			huh.NewInput().
				Title("Reconcile max staleness").
				Description("Duration before marking unseen files as missing").
				Value(&current.Reconcile.MaxStaleness).
				Validate(func(s string) error {
					if s == "" {
						return nil
					}
					_, err := time.ParseDuration(s)
					return err
				}),
		).Title("Options"),
	)

	accessible := !isatty.IsTerminal(os.Stdin.Fd())
	// Huh's accessible mode creates a new bufio.Scanner per field.
	// Each scanner reads ahead, consuming input meant for later fields.
	// A one-byte-at-a-time reader prevents this. Shared across the form
	// and post-form confirm prompts so they read from the same stream.
	var safeReader io.Reader
	if accessible {
		safeReader = &oneByteReader{os.Stdin}
		form = form.WithAccessible(true).WithInput(safeReader)
	}

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return fmt.Errorf("init cancelled")
		}
		return err
	}

	if current.PreProcessHook == "-" {
		current.PreProcessHook = ""
	}

	dirs := []*string{&current.RawDir, &current.CleanDir, &current.TrashDir, &current.DataDir}
	for _, d := range dirs {
		*d, err = expandAndResolve(*d)
		if err != nil {
			return err
		}
	}

	for _, d := range dirs {
		if err := ensureDir(*d, accessible, safeReader); err != nil {
			return err
		}
	}

	if err := current.Validate(); err != nil {
		return err
	}

	if err := config.Save(dest, current); err != nil {
		return err
	}

	fmt.Printf("Config written to %s\n", dest)
	return nil
}

func resolveInitTarget(cfgFlag string) (string, *config.Config, error) {
	path, err := config.Find(cfgFlag)
	if err != nil {
		cfg := &config.Config{
			Reconcile: config.ReconcileConfig{MaxStaleness: config.DefaultMaxStaleness},
		}
		if tz := detectSystemTZ(); tz != "" {
			cfg.Organize.Timezone = tz
		} else {
			cfg.Organize.Timezone = "UTC"
		}
		return config.DefaultConfigPath(), cfg, nil
	}

	cfg, err := config.LoadPermissive(path)
	if err != nil {
		return "", nil, err
	}
	fmt.Printf("Editing config at %s\n", path)
	return path, cfg, nil
}

func dirInput(title string, value *string) *huh.Input {
	return huh.NewInput().
		Title(title).
		Value(value).
		SuggestionsFunc(func() []string {
			return dirSuggestions(*value)
		}, value)
}

func tzInput(value *string, zones []string) *huh.Input {
	return huh.NewInput().
		Title("Timezone").
		Description("IANA timezone for interpreting EXIF timestamps").
		Value(value).
		SuggestionsFunc(func() []string {
			matches := matchZones(*value, zones)
			if len(matches) > 10 {
				matches = matches[:10]
			}
			return matches
		}, value).
		Validate(func(s string) error {
			if s == "" {
				return nil
			}
			if s == "Local" {
				return fmt.Errorf("explicit IANA timezone required (not %q)", s)
			}
			_, err := time.LoadLocation(s)
			return err
		})
}

func dirSuggestions(partial string) []string {
	if partial == "" {
		return nil
	}
	expanded := expandTilde(partial)
	matches, _ := filepath.Glob(expanded + "*")
	var dirs []string
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || !info.IsDir() {
			continue
		}
		display := m
		if strings.HasPrefix(partial, "~/") {
			if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(m, home) {
				display = "~/" + strings.TrimPrefix(m, home+string(filepath.Separator))
			}
		}
		dirs = append(dirs, display+string(filepath.Separator))
	}
	return dirs
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

type oneByteReader struct{ r io.Reader }

func (o *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

func ensureDir(path string, accessible bool, r io.Reader) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	var create bool
	confirm := huh.NewConfirm().
		Title(fmt.Sprintf("%s does not exist. Create it?", path)).
		Value(&create).
		Affirmative("Yes").
		Negative("No")
	if accessible {
		if err := confirm.RunAccessible(os.Stdout, r); err != nil {
			return err
		}
	} else {
		if err := confirm.Run(); err != nil {
			return err
		}
	}
	if !create {
		return fmt.Errorf("directory %s does not exist", path)
	}
	return os.MkdirAll(path, 0o755)
}

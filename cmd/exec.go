package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec COMMAND [args...]",
		Short: "Run a command with winnow's bundled runtime dependencies on PATH",
		Long: `Run a command inheriting winnow's PATH, which includes the bundled
exiftool, file, and ffmpeg binaries when installed via the Nix flake.

Examples:
  winnow exec exiftool -json photo.jpg
  winnow exec ffmpeg -i input.mp4 output.avi
  winnow exec file --mime-type somefile`,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(args)
		},
	}
}

func runExec(args []string) error {
	bin, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("looking up %q: %w", args[0], err)
	}
	c := exec.Command(bin, args[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	err = c.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	return err
}

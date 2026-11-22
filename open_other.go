//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func startAndReap(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func(c *exec.Cmd) {
		_ = c.Wait()
	}(cmd)
	return nil
}

func openExternalBrowserWithOS(safeURL string) error {
	if runtime.GOOS == "darwin" {
		return startAndReap(exec.Command("open", safeURL))
	}
	return startAndReap(exec.Command("xdg-open", safeURL))
}

func openDirectoryWithOS(dir string) error {
	if runtime.GOOS == "darwin" {
		return startAndReap(exec.Command("open", dir))
	}
	return startAndReap(exec.Command("xdg-open", dir))
}

//go:build !windows

package switcher

import "os/exec"

func configureHiddenSubprocess(_ *exec.Cmd) {}

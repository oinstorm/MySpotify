//go:build !windows && !darwin

package spotify

import "runtime"

func clientPlatform() (string, string) { return runtime.GOOS, "unknown" }

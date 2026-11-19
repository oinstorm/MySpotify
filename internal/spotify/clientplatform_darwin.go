package spotify

import "golang.org/x/sys/unix"

func clientPlatform() (string, string) {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		v = "unknown"
	}
	return "macos", v
}

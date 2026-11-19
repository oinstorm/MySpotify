package spotify

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func clientPlatform() (string, string) {
	v := windows.RtlGetVersion()
	return "windows", fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

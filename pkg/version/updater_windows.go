//go:build windows

package version

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

const innoSetupUninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\{8F9C3D52-4A7B-4E91-B2C8-6D5A1F0E3B94}_is1`

// SyncInstalledVersionRegistry updates the Windows Uninstall registry DisplayVersion
// if the application was installed via the Inno Setup installer, so that Windows
// "Installed Apps" stays in sync even after automatic self-updates.
func SyncInstalledVersionRegistry() {
	cleanVer := strings.TrimPrefix(strings.TrimPrefix(CurrentVersion, "v"), "V")
	if cleanVer == "" {
		return
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, innoSetupUninstallKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()

	_ = k.SetStringValue("DisplayVersion", cleanVer)
}

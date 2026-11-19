package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vinyl/internal/spotify"
)

var deviceIdentityDir = accountCookieDir

func configureClientTokens() error {
	dir, err := deviceIdentityDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	device, err := loadDeviceID(filepath.Join(dir, "device-id"))
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "device-credentials")
	var data []byte
	sealed, readErr := os.ReadFile(path)
	if readErr == nil {
		data, readErr = unsealCookies(sealed)
	} else if errors.Is(readErr, os.ErrNotExist) {
		readErr = nil
	}
	err = spotify.ConfigureClientTokens(device, data, func(b []byte) error {
		sealed, err := sealCookies(b)
		if err != nil {
			return err
		}
		return writeAtomic(path, sealed)
	})
	return errors.Join(readErr, err)
}

func loadDeviceID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if raw, err := hex.DecodeString(id); err != nil || len(raw) != 16 {
			return "", fmt.Errorf("device identity: invalid saved ID")
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	if err := writeAtomic(path, []byte(id)); err != nil {
		return "", err
	}
	return id, nil
}

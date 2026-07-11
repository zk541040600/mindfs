package relay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	configpkg "mindfs/server/internal/config"
)

func getOrCreateDeviceID() (string, error) {
	configDir, err := configpkg.MindFSConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(configDir, "device.json")
	var payload struct {
		DeviceID string `json:"device_id"`
	}

	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &payload); err != nil {
			return "", err
		}
		if deviceID := strings.TrimSpace(payload.DeviceID); deviceID != "" {
			return deviceID, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	payload.DeviceID = "md_" + base64.RawURLEncoding.EncodeToString(buf)
	data, err = json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeDeviceFileAtomic(path, data); err != nil {
		return "", err
	}
	return payload.DeviceID, nil
}

func writeDeviceFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

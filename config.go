//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	DisplayIndex      int      `json:"display_index"`
	DisplayID         uint32   `json:"display_id"`
	DisplayName       string   `json:"display_name"`
	ProcessExceptions []string `json:"process_exceptions"`
	DefaultBPC        int      `json:"default_bpc"`
	GameBPC           int      `json:"game_bpc"`
}

var (
	cfg                     Config
	cachedProcessExceptions map[string]struct{}
	configPath              string
	configMu                sync.Mutex
)

func defaultConfig() Config {
	return Config{
		DisplayIndex:      0,
		DisplayID:         0,
		DisplayName:       "",
		ProcessExceptions: []string{},
		DefaultBPC:        nvBPC8,
		GameBPC:           nvBPC10,
	}
}

func loadConfig() error {
	configMu.Lock()
	defer configMu.Unlock()

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	configPath = filepath.Join(filepath.Dir(exePath), "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			cfg = defaultConfig()
			return saveConfigLocked()
		}
		return err
	}

	cfg = defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	rebuildProcessCache()
	return nil
}

func saveConfig() error {
	configMu.Lock()
	defer configMu.Unlock()
	return saveConfigLocked()
}

func saveConfigLocked() error {
	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return err
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, configPath)
}

func rebuildProcessCache() {
	cachedProcessExceptions = make(map[string]struct{}, len(cfg.ProcessExceptions))
	for _, name := range cfg.ProcessExceptions {
		cachedProcessExceptions[strings.ToLower(name)] = struct{}{}
	}
}

func isExceptedProcess(processName string) bool {
	configMu.Lock()
	defer configMu.Unlock()
	_, found := cachedProcessExceptions[strings.ToLower(processName)]
	return found
}

func addProcessException(processName string) error {
	configMu.Lock()
	defer configMu.Unlock()

	lower := strings.ToLower(processName)
	if _, exists := cachedProcessExceptions[lower]; exists {
		return nil
	}

	cfg.ProcessExceptions = append(cfg.ProcessExceptions, processName)
	cachedProcessExceptions[lower] = struct{}{}
	return saveConfigLocked()
}

func removeProcessException(processName string) error {
	configMu.Lock()
	defer configMu.Unlock()

	lower := strings.ToLower(processName)
	delete(cachedProcessExceptions, lower)

	filtered := cfg.ProcessExceptions[:0]
	for _, name := range cfg.ProcessExceptions {
		if strings.ToLower(name) != lower {
			filtered = append(filtered, name)
		}
	}
	cfg.ProcessExceptions = filtered

	return saveConfigLocked()
}

func setDisplay(index int, displayID uint32, displayName string) error {
	configMu.Lock()
	defer configMu.Unlock()

	cfg.DisplayIndex = index
	cfg.DisplayID = displayID
	cfg.DisplayName = displayName
	return saveConfigLocked()
}

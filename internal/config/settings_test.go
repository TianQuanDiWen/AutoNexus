package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsValidationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	mgr, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := mgr.UpdateSettings(&enabled, "sepia"); err == nil {
		t.Fatal("invalid theme was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || mgr.Get().LANEnabled || mgr.Get().Theme != "auto" {
		t.Fatal("invalid settings changed memory or config file")
	}
	if err := mgr.UpdateSettings(&enabled, "light"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Get().LANEnabled || reloaded.Get().Theme != "light" {
		t.Fatal("combined settings did not persist")
	}
	if err := mgr.UpdateSettings(nil, "dark"); err != nil {
		t.Fatal(err)
	}
	if !mgr.Get().LANEnabled || mgr.Get().Theme != "dark" {
		t.Fatal("partial update changed omitted setting")
	}
}

func TestSettingsSaveFailureRestoresMemory(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Writing to a directory reliably fails on every supported platform.
	mgr.filePath = t.TempDir()
	enabled := true
	if err := mgr.UpdateSettings(&enabled, "light"); err == nil {
		t.Fatal("expected write failure")
	}
	if mgr.Get().LANEnabled || mgr.Get().Theme != "auto" {
		t.Fatal("failed save left new settings active in memory")
	}
}

package adapters

import (
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestAppSettingsImportAndPersistence(t *testing.T) {
	t.Setenv("TZ", "America/Lima")
	t.Setenv("REQUEST_TIMEOUT_MS", "45000")
	t.Setenv("COOKIE_SECURE", "true")
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defaults := Config{Timezone: "Europe/Madrid", Timeout: 120 * time.Second}
	saved, err := store.ImportAppSettings(defaults)
	if err != nil || saved.Timezone != "America/Lima" || saved.TimeoutMS != 45000 || !saved.CookieSecure {
		t.Fatal("legacy settings not imported", saved, err)
	}
	desired := domain.AppSettings{Timezone: "UTC", TimeoutMS: 3000}
	if err = store.SaveAppSettings(desired); err != nil {
		t.Fatal(err)
	}
	store.DB.Close()
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	t.Setenv("COOKIE_SECURE", "invalid")
	t.Setenv("REQUEST_TIMEOUT_MS", "invalid")
	t.Setenv("TZ", "invalid")
	reloaded, err := store.ImportAppSettings(defaults)
	if err != nil || reloaded != desired {
		t.Fatal("saved settings overwritten by old environment", reloaded, err)
	}
}

func TestAppSettingsValidation(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	valid := domain.AppSettings{Timezone: "UTC", TimeoutMS: 120000}
	if err = store.SaveAppSettings(valid); err != nil {
		t.Fatal(err)
	}
	for _, settings := range []domain.AppSettings{
		{Timezone: "Local", TimeoutMS: 100}, {Timezone: "", TimeoutMS: 100}, {Timezone: "UTC * *", TimeoutMS: 100},
		{Timezone: "UTC", TimeoutMS: 99}, {Timezone: "UTC", TimeoutMS: 600001},
	} {
		if err = store.SaveAppSettings(settings); err == nil {
			t.Fatal("invalid settings accepted", settings)
		}
	}
	actual, err := store.AppSettings()
	if err != nil || actual != valid {
		t.Fatal("invalid update changed saved settings", actual, err)
	}
}

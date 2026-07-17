package config

import (
	"path/filepath"
	"testing"
)

func TestExtensionConfigurationRoundTrips(t *testing.T) {
	type metricsConfig struct {
		Endpoint string `json:"endpoint"`
		Interval int    `json:"interval"`
	}

	path := filepath.Join(t.TempDir(), "server.json")
	c := Default()
	want := metricsConfig{Endpoint: "https://metrics.example.com", Interval: 30}
	if err := c.SetExtension("metrics-agent", want); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var got metricsConfig
	present, err := loaded.DecodeExtension("metrics-agent", &got)
	if err != nil {
		t.Fatal(err)
	}
	if !present || got != want {
		t.Fatalf("extension = %#v, present=%v; want %#v", got, present, want)
	}

	loaded.RemoveExtension("metrics-agent")
	if present, err := loaded.DecodeExtension("metrics-agent", &got); err != nil || present {
		t.Fatalf("removed extension is still present=%v, err=%v", present, err)
	}
}

func TestExtensionConfigurationRejectsInvalidNames(t *testing.T) {
	c := Default()
	if err := c.SetExtension("Bad Extension", struct{}{}); err == nil {
		t.Fatal("expected an invalid extension name to be rejected")
	}
}

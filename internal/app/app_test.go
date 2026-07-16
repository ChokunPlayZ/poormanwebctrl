package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
)

func TestTUIWritesSelectedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	in := bytes.NewBufferString("2\nblog.example.com\n\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.WebServer.Provider != "apache" {
		t.Fatalf("provider = %q, want apache", c.WebServer.Provider)
	}
	if got := c.Sites[0].Root; got != "/var/www/blog.example.com" {
		t.Fatalf("root = %q", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions are not private: info=%v err=%v", info, err)
	}
}

func TestTUIShowsOperationsForExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("poorman operations")) {
		t.Fatal("operations screen not shown")
	}
	if !bytes.Contains(out.Bytes(), []byte("Firewall management")) {
		t.Fatal("firewall management option not shown")
	}
	if !bytes.Contains(out.Bytes(), []byte("long-term operations")) {
		t.Fatal("long-term operations option not shown")
	}
}

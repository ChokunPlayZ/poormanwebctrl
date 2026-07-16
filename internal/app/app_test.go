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

func TestTUIManagesMultipleVirtualHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("8\n1\nshop.example.com\n\n\nstatic\n\n0\n0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sites) != 2 || c.Sites[1].Domain != "shop.example.com" {
		t.Fatalf("sites = %#v, want second host shop.example.com", c.Sites)
	}
}

func TestTUIConfiguresPostgresReplica(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.json")
	in := bytes.NewBufferString("1\nreplica.example.com\n\n\nphp\npostgresql\nreplica\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || c.Database.Role != "replica" {
		t.Fatalf("database = %#v, want replica", c.Database)
	}
	if got := c.Database.Replication.PrimaryHost; got != "10.20.0.10" {
		t.Fatalf("primary host = %q", got)
	}
}

func TestGuidedReplicaSetupSupportsSameMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("postgresql\n\n\ny\n\n\n\n\n\n\n")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || c.Database.Role != "replica" || c.Database.Port != 5433 || c.Database.Replication.PrimaryHost != "127.0.0.1" || c.Database.Replication.PrimaryPort != 5432 {
		t.Fatalf("database = %#v, want same-machine PostgreSQL replica ports", c.Database)
	}
}

func TestTUIAdjustsStackSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("9\n3\nn\n0\n0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS.Enabled {
		t.Fatal("TLS remained enabled after stack settings update")
	}
}

package ops

import (
	"reflect"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func TestParseAvailableUpdates(t *testing.T) {
	tests := []struct {
		name   string
		family string
		output string
		want   []PackageUpdate
	}{
		{
			name:   "debian",
			family: "debian",
			output: "Listing... Done\nlibssl3/jammy-updates 3.0.2-0ubuntu1.19 amd64 [upgradable from: 3.0.2-0ubuntu1.18]\nnginx/jammy 1.24.0 amd64 [upgradable from: 1.22.1]\n",
			want: []PackageUpdate{
				{Name: "libssl3", Current: "3.0.2-0ubuntu1.18", Available: "3.0.2-0ubuntu1.19"},
				{Name: "nginx", Current: "1.22.1", Available: "1.24.0"},
			},
		},
		{
			name:   "rhel",
			family: "rhel",
			output: "Available Upgrades\nnginx.x86_64 1:1.24.0-2.el9 appstream\nopenssl-libs.x86_64 1:3.2.2-6.el9 baseos\n",
			want: []PackageUpdate{
				{Name: "nginx.x86_64", Available: "1:1.24.0-2.el9"},
				{Name: "openssl-libs.x86_64", Available: "1:3.2.2-6.el9"},
			},
		},
		{
			name:   "alpine",
			family: "alpine",
			output: "musl-1.2.5-r1 x86_64 {main} (MIT) [upgradable from: musl-1.2.5-r0]\npostgresql16-client-16.4-r0 x86_64 {main} (PostgreSQL) [upgradable from: postgresql16-client-16.3-r0]\n",
			want: []PackageUpdate{
				{Name: "musl", Current: "1.2.5-r0", Available: "1.2.5-r1"},
				{Name: "postgresql16-client", Current: "16.3-r0", Available: "16.4-r0"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseAvailableUpdates(tt.family, tt.output); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("updates = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestUpdatePlanTargetsOnlySelectedPackages(t *testing.T) {
	p, err := UpdatePlan(platform.Platform{Distro: "ubuntu", Family: "debian"}, []string{"nginx", "libssl3", "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(p.Steps))
	}
	want := []string{"-o", "Dpkg::Use-Pty=0", "install", "--only-upgrade", "-y", "--", "libssl3", "nginx"}
	if p.Steps[0].Command != "apt-get" || !reflect.DeepEqual(p.Steps[0].Args, want) || !p.Steps[0].NeedsRoot {
		t.Fatalf("update step = %#v", p.Steps[0])
	}
}

func TestUpdatePlanRejectsUnsafePackageName(t *testing.T) {
	if _, err := UpdatePlan(platform.Platform{Family: "debian"}, []string{"nginx;reboot"}); err == nil {
		t.Fatal("unsafe package name was accepted")
	}
}

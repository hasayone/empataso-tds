package config

import "testing"

func TestAdminHostOf(t *testing.T) {
	cases := []struct {
		d    Domain
		want string
	}{
		{Domain{Host: "Example.COM.", Primary: true}, "admin.example.com"},
		{Domain{Host: "a.com", Role: "admin"}, "admin.a.com"},
		{Domain{Host: "a.com", Role: "both"}, "admin.a.com"},
		{Domain{Host: "a.com", Role: "links"}, ""},
		{Domain{Host: "a.com", Primary: true, Disabled: true}, ""},
		{Domain{Host: "admin.a.com", Primary: true}, ""},
	}
	for _, c := range cases {
		if got := AdminHostOf(c.d); got != c.want {
			t.Fatalf("%+v: got %q want %q", c.d, got, c.want)
		}
	}
}

func TestNormalizeHTTPHostPortBeforeDot(t *testing.T) {
	if got := NormalizeHTTPHost("admin.x.com.:443"); got != "admin.x.com" {
		t.Fatalf("got %q", got)
	}
}

func TestAdminHostAllowed(t *testing.T) {
	list := []Domain{
		{Host: "primary.com", Primary: true},
		{Host: "links.com", Role: "links"},
		{Host: "off.com", Primary: true, Disabled: true},
	}
	if !AdminHostAllowed(list, "admin.primary.com") {
		t.Fatal("primary")
	}
	if !AdminHostAllowed(list, "ADMIN.PRIMARY.COM:443") {
		t.Fatal("case and port")
	}
	if !AdminHostAllowed(list, "admin.primary.com.") {
		t.Fatal("trailing dot")
	}
	if AdminHostAllowed(list, "admin.links.com") {
		t.Fatal("links only")
	}
	if AdminHostAllowed(list, "admin.off.com") {
		t.Fatal("disabled")
	}
	if AdminHostAllowed(list, "admin.other.com") {
		t.Fatal("foreign")
	}
	if AdminHostAllowed(nil, "admin.primary.com") {
		t.Fatal("nil list")
	}
	if AdminHostAllowed(list, "primary.com") {
		t.Fatal("not admin host")
	}
}

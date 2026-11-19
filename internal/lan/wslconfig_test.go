package lan

import "testing"

func TestGetKey(t *testing.T) {
	c := "[wsl2]\r\nmemory=4GB\r\n# networkingMode=nat\r\nNetworkingMode = Mirrored\r\n[experimental]\r\nnetworkingMode=x\r\n"
	if got := GetKey(c, "wsl2", "networkingmode"); got != "Mirrored" {
		t.Fatalf("got %q", got)
	}
	if got := GetKey(c, "WSL2", "processors"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := GetKey("\ufeff[wsl2]\nmemory=2GB\n", "wsl2", "memory"); got != "2GB" {
		t.Fatalf("BOM: got %q", got)
	}
}

func TestSetKey(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty file", "", "[wsl2]\nnetworkingMode=mirrored\n"},
		{"append to existing section keeps CRLF",
			"[wsl2]\r\nmemory=4294967296\r\nprocessors=4\r\nvirtiofs=true",
			"[wsl2]\r\nmemory=4294967296\r\nprocessors=4\r\nvirtiofs=true\r\nnetworkingMode=mirrored\r\n"},
		{"replace existing value",
			"[wsl2]\nnetworkingmode=nat\nmemory=4GB\n",
			"[wsl2]\nnetworkingMode=mirrored\nmemory=4GB\n"},
		{"drop duplicates",
			"[wsl2]\nnetworkingMode=nat\nnetworkingMode=none\n",
			"[wsl2]\nnetworkingMode=mirrored\n"},
		{"insert before next section, keep comments",
			"# my settings\n[wsl2]\nmemory=4GB\n\n[experimental]\nautoMemoryReclaim=gradual\n",
			"# my settings\n[wsl2]\nmemory=4GB\nnetworkingMode=mirrored\n\n[experimental]\nautoMemoryReclaim=gradual\n"},
		{"no wsl2 section",
			"[experimental]\nsparseVhd=true\n",
			"[experimental]\nsparseVhd=true\n\n[wsl2]\nnetworkingMode=mirrored\n"},
		{"empty wsl2 section",
			"[wsl2]\n[experimental]\nsparseVhd=true\n",
			"[wsl2]\nnetworkingMode=mirrored\n[experimental]\nsparseVhd=true\n"},
		{"keeps BOM", "\ufeff[wsl2]\nmemory=1GB\n", "\ufeff[wsl2]\nmemory=1GB\nnetworkingMode=mirrored\n"},
		{"commented key is not replaced",
			"[wsl2]\n#networkingMode=mirrored\n",
			"[wsl2]\n#networkingMode=mirrored\nnetworkingMode=mirrored\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SetKey(c.in, "wsl2", "networkingMode", "mirrored"); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestRemoveKey(t *testing.T) {
	in := "[wsl2]\r\nmemory=4GB\r\nnetworkingMode=mirrored\r\n# keep me\r\n[experimental]\r\nnetworkingMode=other\r\n"
	want := "[wsl2]\r\nmemory=4GB\r\n# keep me\r\n[experimental]\r\nnetworkingMode=other\r\n"
	if got := RemoveKey(in, "wsl2", "networkingMode"); got != want {
		t.Fatalf("got %q", got)
	}
	if got := RemoveKey("", "wsl2", "networkingMode"); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestSetThenRemoveRoundTrip(t *testing.T) {
	in := "[wsl2]\r\nmemory=4294967296\r\nprocessors=4\r\nvirtiofs=true\r\n"
	if got := RemoveKey(SetKey(in, "wsl2", "networkingMode", "mirrored"), "wsl2", "networkingMode"); got != in {
		t.Fatalf("got %q", got)
	}
}

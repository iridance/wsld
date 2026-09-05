package i18n

import "testing"

func TestDetectFromWindowsLangID(t *testing.T) {
	cases := map[uint16]Lang{
		0x0404: ZhTW, // zh-TW
		0x0804: ZhTW, // zh-CN → 仍用繁中（SPEC：中文→繁中）
		0x0C04: ZhTW, // zh-HK
		0x0409: EN,   // en-US
		0x0411: EN,   // ja-JP → 英文
		0x0000: EN,
	}
	for id, want := range cases {
		if got := DetectFrom(id, func(string) string { return "" }); got != want {
			t.Errorf("langid %#x: got %s want %s", id, got, want)
		}
	}
}

func TestDetectFromEnvWhenNoLangID(t *testing.T) {
	env := map[string]string{"LC_ALL": "", "LANG": "zh_TW.UTF-8"}
	if got := DetectFrom(0, func(k string) string { return env[k] }); got != ZhTW {
		t.Fatalf("got %s", got)
	}
	env["LANG"] = "en_US.UTF-8"
	if got := DetectFrom(0, func(k string) string { return env[k] }); got != EN {
		t.Fatalf("got %s", got)
	}
	env["LC_ALL"] = "zh_CN.UTF-8"
	if got := DetectFrom(0, func(k string) string { return env[k] }); got != ZhTW {
		t.Fatalf("LC_ALL should win, got %s", got)
	}
}

func TestWSLDLangOverride(t *testing.T) {
	env := map[string]string{"WSLD_LANG": "en", "LANG": "zh_TW.UTF-8"}
	if got := DetectFrom(0x0404, func(k string) string { return env[k] }); got != EN {
		t.Fatalf("override should win, got %s", got)
	}
}

func TestTFallsBackToEnglishThenKey(t *testing.T) {
	Set(ZhTW)
	t.Cleanup(func() { Set(EN) })
	if got := T("cli.exit.not_ready"); got == "" || got == "cli.exit.not_ready" {
		t.Fatalf("zh translation missing: %q", got)
	}
	if got := T("no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key should echo key, got %q", got)
	}
}

func TestTFormats(t *testing.T) {
	Set(EN)
	if got := T("status.distro", "Ubuntu-26.04"); got != "Distro: Ubuntu-26.04" {
		t.Fatalf("got %q", got)
	}
}

func TestAllReturnsMergedMap(t *testing.T) {
	en := All(EN)
	zh := All(ZhTW)
	if len(en) == 0 || len(zh) != len(en) {
		t.Fatalf("len en=%d zh=%d (zh must fall back to en for missing keys)", len(en), len(zh))
	}
}

func TestEveryKeyHasEnglish(t *testing.T) {
	for k, m := range messages {
		if m[EN] == "" {
			t.Errorf("key %s lacks English", k)
		}
	}
}

func TestParseAcceptsKnownLangsOnly(t *testing.T) {
	for in, want := range map[string]Lang{"en": EN, "EN": EN, "zh-TW": ZhTW, "zh-tw": ZhTW, "zh": ZhTW} {
		got, ok := Parse(in)
		if !ok || got != want {
			t.Fatalf("Parse(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := Parse("klingon"); ok {
		t.Fatal("unknown language must be rejected")
	}
	if _, ok := Parse(""); ok {
		t.Fatal("empty must be rejected (caller decides default)")
	}
}

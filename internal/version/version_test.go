package version

import "testing"

func mustTemplate(t *testing.T, pattern string, dated bool) *Template {
	t.Helper()
	tpl, err := Compile(pattern, dated)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}
func mustParse(t *testing.T, tpl *Template, name string) Value {
	t.Helper()
	v, ok := tpl.Parse(name)
	if !ok {
		t.Fatalf("failed to parse %q", name)
	}
	return v
}

func TestNumericOrdering(t *testing.T) {
	tpl := mustTemplate(t, "gpt-{version}-sol", true)
	for _, tt := range []struct {
		a, b string
		cmp  int
	}{
		{"6.10", "6.9", 1}, {"6.2", "6.10", -1}, {"4-5", "4.5", 0}, {"6.1.0", "6.1", 0},
		{"0006.00010", "6.9", 1}, {"6.9999999999999999999999999", "6.10000000000000000000000000", -1},
	} {
		a := mustParse(t, tpl, "gpt-"+tt.a+"-sol")
		b := mustParse(t, tpl, "gpt-"+tt.b+"-sol")
		if cmp, known := Compare(a, b); !known || cmp != tt.cmp {
			t.Errorf("%s vs %s = %d,%v", tt.a, tt.b, cmp, known)
		}
	}
}

func TestWholeFamilyAndPrerelease(t *testing.T) {
	tpl := mustTemplate(t, "gpt-{version}-sol", true)
	for _, name := range []string{"gpt-6.1-sol-mini", "gpt-6.1-sol-codex", "gpt-6.1-sol(8192)", "gpt-6.1-sol-vision", "xgpt-6.1-sol", "gpt-6.x-sol", "gpt-6.1-sol-beta-other"} {
		if _, ok := tpl.Parse(name); ok {
			t.Errorf("derivative/invalid matched: %s", name)
		}
	}
	for _, marker := range []string{"preview", "beta", "alpha", "exp", "experimental", "rc", "nightly", "dev", "test", "preview-2", "rc1"} {
		v := mustParse(t, tpl, "GPT-6.1-SOL-"+marker)
		if v.Prerelease == "" {
			t.Errorf("missing prerelease for %s", marker)
		}
	}
	stable := mustParse(t, tpl, "gpt-6.1-sol")
	pre := mustParse(t, tpl, "gpt-6.1-sol-preview")
	if cmp, ok := Compare(stable, pre); !ok || cmp != 1 {
		t.Fatalf("stable vs pre %d,%v", cmp, ok)
	}
}

func TestDatesAndAmbiguity(t *testing.T) {
	tpl := mustTemplate(t, "claude-opus-{version}", true)
	v := mustParse(t, tpl, "claude-opus-4-5-20251101")
	if v.Version != "4-5" || v.Date != "20251101" {
		t.Fatalf("date swallowed by version: %+v", v)
	}
	other := mustParse(t, tpl, "claude-opus-4.5-2025-12-01")
	if cmp, ok := Compare(other, v); !ok || cmp != 1 {
		t.Fatalf("date order %d,%v", cmp, ok)
	}
	undated := mustParse(t, tpl, "claude-opus-4-5")
	if _, ok := Compare(undated, v); ok {
		t.Fatal("dated/undated must be ambiguous")
	}
	for _, name := range []string{"claude-opus-4-5-20250230", "claude-opus-4-5-2025-13-01", "claude-opus-4-5-1101", "claude-opus-4-5-251101"} {
		if _, ok := tpl.Parse(name); ok {
			t.Errorf("invalid or ambiguous date matched: %s", name)
		}
	}
	explicit := mustTemplate(t, "claude-opus-{version}-{date}", false)
	if x := mustParse(t, explicit, "claude-opus-4-5-251101"); x.Version != "4-5" || x.Date != "251101" {
		t.Fatalf("%+v", x)
	}
	short := mustTemplate(t, "gpt-{version}-sol", true)
	mmdd := mustParse(t, short, "gpt-6.1-sol-1101")
	mmdd2 := mustParse(t, short, "gpt-6.1-sol-1201")
	if _, ok := Compare(mmdd, mmdd2); ok {
		t.Fatal("MMDD cannot invent a shared year")
	}
	yy := mustParse(t, short, "gpt-6.1-sol-251101")
	yy2 := mustParse(t, short, "gpt-6.1-sol-251201")
	if cmp, ok := Compare(yy, yy2); !ok || cmp != -1 {
		t.Fatalf("shared short year %d,%v", cmp, ok)
	}
	yy3 := mustParse(t, short, "gpt-6.1-sol-261201")
	if _, ok := Compare(yy, yy3); ok {
		t.Fatal("short dates cannot invent a century across years")
	}
}

func TestTemplateValidationAndLiteralEscaping(t *testing.T) {
	for _, pattern := range []string{"", "gpt-sol", "gpt-{version}-{version}", "gpt-{version}-{other}", "gpt-{version}{date}", "gpt-*-{version}", "gpt/{version}", " gpt-{version}"} {
		if _, err := Compile(pattern, true); err == nil {
			t.Errorf("accepted invalid template %q", pattern)
		}
	}
	tpl := mustTemplate(t, "model+{version}.sol", false)
	mustParse(t, tpl, "MODEL+6.1.SOL")
	for _, name := range []string{"modell6.1.sol", "model+6.1Xsol", "model+6.1.sol-20251101"} {
		if _, ok := tpl.Parse(name); ok {
			t.Errorf("literal/allow_dated bypass: %s", name)
		}
	}
	// Explicit dates are permitted even when optional trailing dates are disabled.
	mustParse(t, mustTemplate(t, "gpt-{date}-sol-{version}", false), "gpt-20251101-sol-6.10")
}

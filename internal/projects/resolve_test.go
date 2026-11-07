package projects

import "testing"

func candidates(items ...[2]string) []InstalledRuntime {
	var out []InstalledRuntime
	for _, pair := range items {
		out = append(out, InstalledRuntime{Version: pair[0], FullVersion: pair[1]})
	}
	return out
}

func TestResolvePicksHighestSatisfying(t *testing.T) {
	inst := candidates(
		[2]string{"8.4", "8.4.25"},
		[2]string{"8.3", "8.3.36"},
	)

	res := Resolve("^8.3", "composer.json", false, inst, nil)
	if res.Status != StatusResolved || res.Version != "8.4" {
		t.Fatalf("^8.3 → %+v, want 8.4 resolved", res)
	}

	res = Resolve("~8.3", "composer.json", false, inst, nil)
	if res.Status != StatusResolved || res.Version != "8.3" {
		t.Fatalf("~8.3 → %+v, want 8.3 resolved", res)
	}
}

func TestResolveComposerBareVersionIsExact(t *testing.T) {
	inst := candidates([2]string{"8.3", "8.3.36"})

	// Composer semantics: "8.3" is exactly 8.3.0 — 8.3.36 does NOT satisfy.
	res := Resolve("8.3", "composer.json", false, inst, nil)
	if res.Status != StatusMissing {
		t.Fatalf(`composer "8.3" vs 8.3.36 → %+v, want missing`, res)
	}

	// A real 8.3.0 satisfies.
	res = Resolve("8.3", "composer.json", false, candidates([2]string{"8.3", "8.3.0"}), nil)
	if res.Status != StatusResolved {
		t.Fatalf(`composer "8.3" vs 8.3.0 → %+v, want resolved`, res)
	}
}

func TestResolvePhpVersionFileMeansSeries(t *testing.T) {
	inst := candidates([2]string{"8.3", "8.3.36"})

	// .php-version semantics: "8.3" is the series — 8.3.36 satisfies.
	res := Resolve("8.3", ".php-version", true, inst, nil)
	if res.Status != StatusResolved || res.Version != "8.3" {
		t.Fatalf(`.php-version "8.3" → %+v, want series resolved`, res)
	}
}

func TestResolveRangesAndOrBranches(t *testing.T) {
	inst := candidates(
		[2]string{"8.5", "8.5.12"},
		[2]string{"8.1", "8.1.34"},
	)

	res := Resolve(">=8.1 <8.4", "composer.json", false, inst, nil)
	if res.Status != StatusResolved || res.Version != "8.1" {
		t.Fatalf("range → %+v, want 8.1", res)
	}

	// ^7.4 || ^8.1 spans every 8.x from 8.1 up — 8.5.12 is the newest hit.
	res = Resolve("^7.4 || ^8.1", "composer.json", false, inst, nil)
	if res.Status != StatusResolved || res.Version != "8.5" {
		t.Fatalf("or-branch → %+v, want 8.5", res)
	}

	res = Resolve("^7.4", "composer.json", false, inst, nil)
	if res.Status != StatusMissing {
		t.Fatalf("^7.4 → %+v, want missing", res)
	}
}

func TestResolveSuggestsAnInstallableVersion(t *testing.T) {
	catalog := candidates(
		[2]string{"8.4", "8.4.25"},
		[2]string{"8.3", "8.3.36"},
	)
	res := Resolve("^8.3 || ^8.4", "composer.json", false, nil, catalog)
	if res.Status != StatusMissing || res.InstallVersion != "8.4" {
		t.Fatalf("suggestion → %+v, want missing + install 8.4", res)
	}
}

func TestResolveEmptyConstraint(t *testing.T) {
	inst := candidates([2]string{"24.22.2", "24.22.2"})
	res := Resolve("", "", false, inst, nil)
	if res.Status != StatusResolved || res.Version != "24.22.2" {
		t.Fatalf("no constraint → %+v, want newest resolved", res)
	}
	if res2 := Resolve("", "", false, nil, nil); res2.Status != StatusUnmanaged {
		t.Fatalf("nothing anywhere → %+v, want unmanaged", res2)
	}
}

func TestResolveInvalidConstraintIsReportedNotGuessed(t *testing.T) {
	res := Resolve("php >=", "composer.json", false, candidates([2]string{"8.4", "8.4.25"}), nil)
	if res.Status != StatusInvalid || res.Problem == "" {
		t.Fatalf("garbage → %+v, want invalid with a problem", res)
	}
}

func TestOverrideMustNotContradictTheDeclaration(t *testing.T) {
	inst := candidates([2]string{"8.1", "8.1.34"}, [2]string{"8.4", "8.4.25"})

	res := CheckOverride("^8.3", false, "8.4", inst)
	if res.Status != StatusResolved {
		t.Fatalf("compatible override → %+v", res)
	}
	res = CheckOverride("^8.3", false, "8.1", inst)
	if res.Status != StatusIncompatible {
		t.Fatalf("contradicting override → %+v, want incompatible", res)
	}
	// With no declaration, any installed pin is fine.
	res = CheckOverride("", false, "8.1", inst)
	if res.Status != StatusResolved {
		t.Fatalf("free override → %+v", res)
	}
}

func TestSeriesWildcards(t *testing.T) {
	// Single-field "8" matches any 8.x under either semantics.
	inst := candidates([2]string{"8.4", "8.4.25"}, [2]string{"7.4", "7.4.33"})
	if res := Resolve("8", "composer.json", false, inst, nil); res.Status != StatusResolved || res.Version != "8.4" {
		t.Fatalf(`"8" → %+v`, res)
	}
	// Prerelease-tagged binaries still parse as their numeric base.
	if res := Resolve("~8.4", "composer.json", false, candidates([2]string{"8.4", "8.4.25-dev"}), nil); res.Status != StatusResolved {
		t.Fatalf("dev build → %+v", res)
	}
}

package projects

// The runtime constraint engine. A project declares what it needs
// (composer.json's require.php, .php-version, .nvmrc, engines.node) and
// this package decides which installed version satisfies it — without
// silently substituting anything: a declared need nothing satisfies is
// reported, with the version to install named.
//
// Grammar (the honest subset real projects use):
//
//	branch   := compare ("," compare)*        AND, all must hold
//	full     := branch ("||" branch)*         OR, any may hold
//	compare  := op? version                    bare version: exact
//	op       := ">=" | "<=" | ">" | "<" | "!=" | "=" | "==" | "^" | "~"
//	version  := digits ("." digits)* up to 3 fields; "8.4" means 8.4.0
//
// Source semantics differ where reality does: composer's bare "8.3" is
// exactly 8.3.0, while .php-version's "8.3" means the 8.3 series — the
// caller picks with SeriesMeansRange.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Resolution statuses.
const (
	StatusResolved     = "resolved"     // an installed version satisfies
	StatusMissing      = "missing"      // nothing satisfies, but a version exists to install
	StatusUnmanaged    = "unmanaged"    // no declaration and nothing installed
	StatusInvalid      = "invalid"      // the declaration itself is unreadable
	StatusIncompatible = "incompatible" // manual override conflicts with the declaration
)

// Resolution is the verdict for one runtime.
type Resolution struct {
	Status string `json:"status"`
	// Version is the winning installed version ("" unless resolved).
	Version string `json:"version,omitempty"`
	// FullVersion is the winning installed binary's full version.
	FullVersion string `json:"fullVersion,omitempty"`
	// Constraint echoes the declaration, if any.
	Constraint string `json:"constraint,omitempty"`
	// Source names where the declaration came from.
	Source string `json:"source,omitempty"`
	// InstallVersion names what to install when status is missing.
	InstallVersion string `json:"installVersion,omitempty"`
	// Problem explains invalid/incompatible verdicts.
	Problem string `json:"problem,omitempty"`
}

// InstalledRuntime is one candidate: the manager's series or full version
// plus the binary's full version.
type InstalledRuntime struct {
	Version     string // series for PHP ("8.4"), full for Node ("24.22.2")
	FullVersion string
}

// Resolve picks the best installed candidate for a constraint.
// Candidates arrive newest-first (managers already sort); "" constraint
// means "nothing declared" → highest installed wins (StatusUnmanaged when
// none are installed either). catalog is the list of installable versions
// (newest-first) used to fill InstallVersion on a miss.
func Resolve(constraint, source string, seriesIsRange bool, candidates, catalog []InstalledRuntime) Resolution {
	res := Resolution{Constraint: constraint, Source: source}

	if constraint == "" {
		if len(candidates) == 0 {
			res.Status = StatusUnmanaged
			return res
		}
		res.Status = StatusResolved
		res.Version = candidates[0].Version
		res.FullVersion = candidates[0].FullVersion
		return res
	}

	branches, err := parseConstraint(constraint)
	if err != nil {
		res.Status = StatusInvalid
		res.Problem = err.Error()
		return res
	}

	// Highest installed satisfying candidate wins.
	for _, candidate := range candidates {
		if versionSatisfies(candidate.FullVersion, branches, seriesIsRange) {
			res.Status = StatusResolved
			res.Version = candidate.Version
			res.FullVersion = candidate.FullVersion
			return res
		}
	}

	res.Status = StatusMissing
	for _, offer := range catalog {
		if versionSatisfies(offer.FullVersion, branches, seriesIsRange) {
			res.InstallVersion = offer.Version
			return res
		}
	}
	return res
}

// CheckOverride validates a manual override against the project's
// declaration: the user may pin anything they like, as long as it does not
// contradict what the project itself demands.
func CheckOverride(constraint string, seriesIsRange bool, override string, installed []InstalledRuntime) Resolution {
	res := Resolution{Constraint: constraint, Version: override, Source: "manual"}
	full := ""
	for _, candidate := range installed {
		if candidate.Version == override {
			full = candidate.FullVersion
			break
		}
	}
	res.FullVersion = full

	if constraint == "" {
		res.Status = StatusResolved
		return res
	}
	branches, err := parseConstraint(constraint)
	if err != nil {
		res.Status = StatusInvalid
		res.Problem = err.Error()
		return res
	}
	if versionSatisfies(full, branches, seriesIsRange) {
		res.Status = StatusResolved
		return res
	}
	res.Status = StatusIncompatible
	res.Problem = fmt.Sprintf("%s does not satisfy %s", override, constraint)
	return res
}

// --- parsing ---------------------------------------------------------------

type comparator struct {
	op                  string
	major, minor, patch int
	fields              int // how many fields were written (1..3)
}

var tokenPattern = regexp.MustCompile(`(>=|<=|!=|==|\^|~|>|<|=)?\s*v?(\d+(?:\.\d+){0,2})`)

func parseConstraint(constraint string) ([][]comparator, error) {
	var branches [][]comparator
	for _, raw := range strings.Split(constraint, "||") {
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil, fmt.Errorf("empty branch in %q", constraint)
		}
		var branch []comparator
		for _, token := range splitAnd(text) {
			match := tokenPattern.FindStringSubmatch(strings.TrimSpace(token))
			if match == nil {
				return nil, fmt.Errorf("cannot read %q (in %q)", token, constraint)
			}
			c, err := parseComparator(match[1], match[2])
			if err != nil {
				return nil, fmt.Errorf("%q: %v", constraint, err)
			}
			branch = append(branch, c)
		}
		if len(branch) == 0 {
			return nil, fmt.Errorf("empty branch in %q", constraint)
		}
		branches = append(branches, branch)
	}
	return branches, nil
}

// splitAnd splits a branch into its AND-separated comparators: composer
// writes both ">=8.1,<8.4" and ">=8.1 <8.4", so commas and whitespace
// both separate.
func splitAnd(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var out []string
	for _, field := range fields {
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func parseComparator(op, version string) (comparator, error) {
	c := comparator{op: op}
	fields := strings.Split(version, ".")
	if len(fields) > 3 {
		return c, fmt.Errorf("too many fields in %q", version)
	}
	nums := make([]int, 3)
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			return c, fmt.Errorf("bad number %q", field)
		}
		nums[i] = n
	}
	c.major, c.minor, c.patch = nums[0], nums[1], nums[2]
	c.fields = len(fields)
	return c, nil
}

// versionSatisfies reports whether full (a 3-field version string) holds
// for every branch alternative.
func versionSatisfies(full string, branches [][]comparator, seriesIsRange bool) bool {
	parts := strings.Split(full, ".")
	nums := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		nums[i], _ = strconv.Atoi(parts[i])
	}
	for _, branch := range branches {
		ok := true
		for _, c := range branch {
			if !holds(c, nums, seriesIsRange) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func pad(fields [3]int, count int) [3]int {
	// Shorter comparators compare only their written fields.
	return fields
}

func holds(c comparator, have [3]int, seriesIsRange bool) bool {
	// A single-field comparator like "8" matches any 8.x.y.
	eq := have == [3]int{c.major, c.minor, c.patch}
	_ = eq
	switch c.op {
	case "^":
		// Compatible: same leading nonzero field, not below.
		if c.major > 0 {
			return have[0] == c.major && atLeast(have, c)
		}
		return have[1] == c.minor && atLeast(have, c)
	case "~":
		if c.fields >= 2 {
			return have[0] == c.major && have[1] == c.minor && atLeast(have, c)
		}
		return have[0] == c.major && atLeast(have, c)
	case ">=":
		return ge(have, c)
	case "<=":
		return le(have, c)
	case ">":
		return gt(have, c)
	case "<":
		return lt(have, c)
	case "!=", "<>":
		return !eq
	case "=", "==", "":
		// Bare/equals: compare exactly the written fields.
		switch c.fields {
		case 1:
			return have[0] == c.major
		case 2:
			if seriesIsRange {
				// ".php-version 8.4" = the 8.4 series.
				return have[0] == c.major && have[1] == c.minor
			}
			return have[0] == c.major && have[1] == c.minor && have[2] == c.patch
		default:
			return eq
		}
	}
	return false
}

func atLeast(have [3]int, c comparator) bool { return ge(have, c) }

func cmp(have [3]int, c comparator) int {
	other := [3]int{c.major, c.minor, c.patch}
	for i := 0; i < 3; i++ {
		if have[i] != other[i] {
			if have[i] > other[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func ge(have [3]int, c comparator) bool { return cmp(have, c) >= 0 }
func le(have [3]int, c comparator) bool { return cmp(have, c) <= 0 }
func gt(have [3]int, c comparator) bool { return cmp(have, c) > 0 }
func lt(have [3]int, c comparator) bool { return cmp(have, c) < 0 }

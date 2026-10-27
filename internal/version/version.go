// Package version implements whole-name family matching and conservative ordering.
package version

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Value is a parsed family version. Components are decimal strings so unusually
// large version numbers cannot overflow and silently reverse the ordering.
type Value struct {
	Version    string   `json:"version"`
	Components []string `json:"-"`
	Date       string   `json:"date,omitempty"`
	Prerelease string   `json:"prerelease,omitempty"`
}

type Template struct {
	base                 *regexp.Regexp
	dated                *regexp.Regexp
	explicitDate         bool
	allowDated           bool
	shortDateUnambiguous bool
}

const versionRE = `([0-9]+(?:[.-][0-9]+)*)`
const dateRE = `([0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{8}|[0-9]{6}|[0-9]{4})`
const prereleaseRE = `(?:-(preview|beta|alpha|exp|experimental|rc|nightly|dev|test)(?:[.-]?([0-9]+))?)?`

// Compile accepts exactly one version token and at most one date token.
// Braces, globs, slashes, whitespace, and thinking suffixes are not literals in
// family templates. All other literal text is regexp-escaped.
func Compile(template string, allowDated bool) (*Template, error) {
	if template == "" || strings.TrimSpace(template) != template || strings.Count(template, "{version}") != 1 || strings.Count(template, "{date}") > 1 {
		return nil, errors.New("template requires exactly one {version} and at most one {date}")
	}
	rest := strings.ReplaceAll(strings.ReplaceAll(template, "{version}", ""), "{date}", "")
	if strings.ContainsAny(rest, "{}*?/()\\") || strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || strings.Contains(template, "{version}{date}") || strings.Contains(template, "{date}{version}") {
		return nil, errors.New("invalid template literal or adjacent tokens")
	}
	// Named captures let tokens appear in either order without positional guesses.
	expression := regexp.QuoteMeta(template)
	expression = strings.ReplaceAll(expression, regexp.QuoteMeta("{version}"), `(?P<version>`+versionRE+`)`)
	expression = strings.ReplaceAll(expression, regexp.QuoteMeta("{date}"), `(?P<date>`+dateRE+`)`)
	t := &Template{explicitDate: strings.Contains(template, "{date}"), allowDated: allowDated, shortDateUnambiguous: !strings.HasSuffix(template, "{version}")}
	var err error
	t.base, err = regexp.Compile(`(?i)^` + expression + prereleaseRE + `$`)
	if err != nil {
		return nil, errors.New("invalid family expression")
	}
	if !t.explicitDate {
		t.dated, err = regexp.Compile(`(?i)^` + expression + `-(?P<date>` + dateRE + `)` + prereleaseRE + `$`)
		if err != nil {
			return nil, errors.New("invalid dated family expression")
		}
	}
	return t, nil
}

func (t *Template) Parse(id string) (Value, bool) {
	if t == nil {
		return Value{}, false
	}
	// Recognize dated forms even when disabled: a greedy dashed version must
	// never swallow a date (including an invalid or ambiguous calendar suffix).
	if t.dated != nil {
		if t.dated.MatchString(id) {
			if !t.allowDated {
				return Value{}, false
			}
			v, ok := parse(t.dated, id)
			if !ok {
				return Value{}, false
			}
			if len(v.Date) < 8 && !t.shortDateUnambiguous {
				return Value{}, false
			}
			return v, true
		}
	}
	return parse(t.base, id)
}

func parse(re *regexp.Regexp, id string) (Value, bool) {
	m := re.FindStringSubmatch(id)
	if m == nil {
		return Value{}, false
	}
	v := Value{Version: m[re.SubexpIndex("version")]}
	for _, part := range strings.FieldsFunc(v.Version, func(r rune) bool { return r == '.' || r == '-' }) {
		// Calendar dates belong only in the date capture, never in a dotted
		// or dashed version component. Check the raw length before stripping
		// zeros so other large numeric versions keep their existing semantics.
		if len(part) == 8 && validDate(part) {
			return Value{}, false
		}
		part = strings.TrimLeft(part, "0")
		if part == "" {
			part = "0"
		}
		v.Components = append(v.Components, part)
	}
	if i := re.SubexpIndex("date"); i >= 0 {
		v.Date = strings.ReplaceAll(m[i], "-", "")
		if !validDate(v.Date) {
			return Value{}, false
		}
	}
	// The prerelease captures are always the last two captures.
	v.Prerelease = strings.ToLower(m[len(m)-2])
	if m[len(m)-1] != "" {
		number := strings.TrimLeft(m[len(m)-1], "0")
		if number == "" {
			number = "0"
		}
		v.Prerelease += "." + number
	}
	return v, true
}

func validDate(s string) bool {
	var input string
	switch len(s) {
	case 8:
		input = s
	case 6:
		input = "20" + s // validation only; Compare never assumes this century.
	case 4:
		input = "2000" + s // leap year allows February 29 without assuming a year.
	default:
		return false
	}
	d, err := time.Parse("20060102", input)
	return err == nil && d.Year() > 0
}

// Compare returns an ordering and whether that ordering is known. Same-version
// dated/undated pairs and dates with different precision are incomparable. Short
// dates never supply an invented year or century. Stable beats prerelease for
// the same numeric version/date; distinct prerelease labels are incomparable.
func Compare(a, b Value) (int, bool) {
	n := len(a.Components)
	if len(b.Components) > n {
		n = len(b.Components)
	}
	for i := 0; i < n; i++ {
		x, y := "0", "0"
		if i < len(a.Components) {
			x = a.Components[i]
		}
		if i < len(b.Components) {
			y = b.Components[i]
		}
		if len(x) < len(y) {
			return -1, true
		}
		if len(x) > len(y) {
			return 1, true
		}
		if x < y {
			return -1, true
		}
		if x > y {
			return 1, true
		}
	}
	if a.Date != b.Date {
		if a.Date == "" || b.Date == "" || len(a.Date) != len(b.Date) {
			return 0, false
		}
		// YYMMDD is only orderable within the same explicit short year. MMDD
		// cannot establish that two releases belong to the same year.
		if len(a.Date) == 4 || (len(a.Date) == 6 && a.Date[:2] != b.Date[:2]) {
			return 0, false
		}
		if a.Date < b.Date {
			return -1, true
		}
		return 1, true
	}
	if a.Prerelease == b.Prerelease {
		return 0, true
	}
	if a.Prerelease == "" {
		return 1, true
	}
	if b.Prerelease == "" {
		return -1, true
	}
	return 0, false
}

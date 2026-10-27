package mgmt

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/suk-6/cpa-model-dock/internal/policy"
)

type object map[string]json.RawMessage

const maxBody = 1 << 20

func decodeObject(body []byte) (object, []policy.Detail) {
	if len(body) > maxBody {
		return nil, []policy.Detail{detail("body", "body_too_large", "Management request is too large", map[string]any{"max_bytes": maxBody})}
	}
	var obj object
	if len(bytes.TrimSpace(body)) == 0 {
		return object{}, nil
	}
	if json.Unmarshal(body, &obj) != nil || obj == nil {
		return nil, []policy.Detail{detail("body", "invalid_json", "Expected one JSON object")}
	}
	return obj, nil
}

func field(obj object, name string, out any) []policy.Detail {
	raw, ok := obj[name]
	if !ok {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, out) != nil {
		return []policy.Detail{detail(name, "invalid_type", "Invalid field value", map[string]any{"expected": expectedType(out)})}
	}
	// encoding/json accepts null for scalar Go fields. Reject it explicitly so a
	// malformed draft cannot silently turn a security setting into its zero value.
	var value any
	_ = json.Unmarshal(raw, &value)
	var details []policy.Detail
	var walk func(any, string)
	walk = func(value any, path string) {
		switch value := value.(type) {
		case nil:
			if strings.HasPrefix(path, "aliases[") && strings.HasSuffix(path, "].rewrite_response_model") {
				return // This override is explicitly nullable in the contract.
			}
			details = append(details, detail(path, "invalid_type", "Field must not be null", map[string]any{"expected": "non_null"}))
		case map[string]any:
			for _, key := range fieldNames(value) {
				walk(value[key], path+"."+key)
			}
		case []any:
			for i, child := range value {
				walk(child, path+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	walk(value, name)
	return details
}

func revision(headers http.Header, obj object) (int64, []policy.Detail) {
	var bodyRevision, headerRevision int64
	raw, hasBody := obj["revision"]
	if hasBody && (bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &bodyRevision) != nil || bodyRevision < 0) {
		return 0, []policy.Detail{detail("revision", "invalid_revision_body", "Revision must be a nonnegative integer")}
	}
	var matches []string
	for name, values := range headers {
		if strings.EqualFold(name, "If-Match") {
			matches = append(matches, values...)
		}
	}
	hasHeader := len(matches) > 0
	if hasHeader {
		value := strings.TrimSpace(matches[0])
		quoted := len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"'
		if quoted {
			value = value[1 : len(value)-1]
		}
		valid := quoted && value != "" && len(matches) == 1
		for _, c := range value {
			valid = valid && c >= '0' && c <= '9'
		}
		var err error
		headerRevision, err = strconv.ParseInt(value, 10, 64)
		if !valid || err != nil {
			return 0, []policy.Detail{detail("revision", "invalid_revision_header", "If-Match must contain one revision")}
		}
	}
	if !hasBody && !hasHeader {
		return 0, []policy.Detail{detail("revision", "required_revision", "A revision or If-Match header is required")}
	}
	if hasBody && hasHeader && bodyRevision != headerRevision {
		return 0, []policy.Detail{detail("revision", "revision_disagreement", "Revision and If-Match disagree")}
	}
	if hasBody {
		return bodyRevision, nil
	}
	return headerRevision, nil
}

func fullState(obj object, current policy.State) (policy.State, []policy.Detail) {
	var details []policy.Detail
	for _, key := range []string{"settings", "families", "aliases", "rules"} {
		if _, ok := obj[key]; !ok {
			details = append(details, detail(key, "required_full_state_field", "Field is required for a full state replacement"))
		}
	}
	if raw, ok := obj["settings"]; ok {
		var settings object
		if json.Unmarshal(raw, &settings) == nil && settings != nil {
			for _, name := range []string{"whitelist_enabled", "rewrite_response_model", "refresh_interval_seconds"} {
				if _, ok := settings[name]; !ok {
					details = append(details, detail("settings."+name, "required_setting", "Setting is required"))
				}
			}
		}
	}
	base := policy.State{Schema: current.Schema, Revision: current.Revision, UpdatedAt: current.UpdatedAt}
	draft, patchDetails := draftState(obj, base)
	details = append(details, patchDetails...)
	if len(details) == 0 {
		details = assignIDs(&draft)
	}
	return draft, details
}

// Only editable fields participate in parsing; unknown and runtime fields are ignored.
func draftState(obj object, saved policy.State) (policy.State, []policy.Detail) {
	var details []policy.Detail
	if raw, ok := obj["settings"]; ok {
		var settings object
		if json.Unmarshal(raw, &settings) != nil || settings == nil {
			details = append(details, detail("settings", "invalid_type", "Settings must be an object", map[string]any{"expected": "object"}))
		} else {
			settingsFields := map[string]any{"whitelist_enabled": &saved.Settings.WhitelistEnabled,
				"rewrite_response_model": &saved.Settings.RewriteResponseModel, "refresh_interval_seconds": &saved.Settings.RefreshIntervalSeconds}
			for _, name := range fieldNames(settingsFields) {
				target := settingsFields[name]
				for _, d := range field(settings, name, target) {
					d.Path = "settings." + d.Path
					details = append(details, d)
				}
			}
		}
	}
	for _, section := range []string{"families", "aliases", "rules"} {
		raw, ok := obj[section]
		if !ok {
			continue
		}
		var entries []object
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &entries) != nil {
			details = append(details, detail(section, "invalid_type", "Expected an array of objects", map[string]any{"expected": "array_of_objects"}))
			continue
		}
		switch section {
		case "families":
			saved.Families = []policy.Family{}
		case "aliases":
			saved.Aliases = []policy.Alias{}
		case "rules":
			saved.Rules = []policy.Rule{}
		}
		for i, entry := range entries {
			path := section + "[" + strconv.Itoa(i) + "]"
			if entry == nil {
				details = append(details, detail(path, "invalid_type", "Expected an object", map[string]any{"expected": "object"}))
				continue
			}
			f, a, r := policy.Family{AllowDated: true}, policy.Alias{Enabled: true}, policy.Rule{Enabled: true}
			var fields map[string]any
			switch section {
			case "families":
				fields = map[string]any{"id": &f.ID, "name": &f.Name, "template": &f.Template, "provider": &f.Provider, "allow_dated": &f.AllowDated}
			case "aliases":
				fields = map[string]any{"id": &a.ID, "name": &a.Name, "mode": &a.Mode, "target": &a.Target, "provider": &a.Provider, "enabled": &a.Enabled, "expose_original": &a.ExposeOriginal, "rewrite_response_model": &a.RewriteResponseModel}
			case "rules":
				fields = map[string]any{"id": &r.ID, "enabled": &r.Enabled, "kind": &r.Kind, "value": &r.Value, "note": &r.Note}
			}
			for _, name := range fieldNames(fields) {
				target := fields[name]
				if name == "rewrite_response_model" && bytes.Equal(bytes.TrimSpace(entry[name]), []byte("null")) {
					continue
				}
				for _, d := range field(entry, name, target) {
					d.Path = path + "." + d.Path
					details = append(details, d)
				}
			}
			if section == "aliases" {
				if raw, ok := entry["auto"]; ok {
					var auto object
					if json.Unmarshal(raw, &auto) != nil || auto == nil {
						details = append(details, detail(path+".auto", "invalid_type", "Expected an object", map[string]any{"expected": "object"}))
					} else {
						autoFields := map[string]any{"family": &a.Auto.Family, "include_prerelease": &a.Auto.IncludePrerelease, "health_check": &a.Auto.HealthCheck}
						for _, name := range fieldNames(autoFields) {
							target := autoFields[name]
							for _, d := range field(auto, name, target) {
								d.Path = path + ".auto." + d.Path
								details = append(details, d)
							}
						}
					}
				}
			}
			switch section {
			case "families":
				saved.Families = append(saved.Families, f)
			case "aliases":
				saved.Aliases = append(saved.Aliases, a)
			case "rules":
				saved.Rules = append(saved.Rules, r)
			}
		}
	}
	return saved, details
}

// ValidateState permits offline targets to remain repairable, while preserving all
// structural, collision and reference checks in the shared policy engine.
func ValidateState(state policy.State, catalog policy.Catalog) []policy.Detail {
	details := []policy.Detail{}
	for _, d := range policy.Validate(state, catalog) {
		if d.Code != "unknown_target" {
			details = append(details, d)
		}
	}
	for i, a := range state.Aliases {
		if strings.Contains(a.Name, "/") {
			details = append(details, detail(fmt.Sprintf("aliases[%d].name", i), "invalid_alias_slash", "Alias names cannot contain a slash"))
		}
	}
	return detailViews(details)
}

func assignIDs(state *policy.State) []policy.Detail {
	var details []policy.Detail
	assign := func(id *string, kind, prefix string, index int) {
		path := kind + "[" + strconv.Itoa(index) + "].id"
		if *id != "" {
			return
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			details = append(details, detail(path, "id_unavailable", "Could not generate an entry ID"))
			return
		}
		token[6] = (token[6] & 0x0f) | 0x40
		token[8] = (token[8] & 0x3f) | 0x80
		*id = prefix + fmt.Sprintf("%x-%x-%x-%x-%x", token[:4], token[4:6], token[6:8], token[8:10], token[10:])
	}
	for i := range state.Families {
		assign(&state.Families[i].ID, "families", "fam_", i)
	}
	for i := range state.Aliases {
		assign(&state.Aliases[i].ID, "aliases", "al_", i)
	}
	for i := range state.Rules {
		assign(&state.Rules[i].ID, "rules", "r_", i)
	}
	return details
}

func fieldNames[T any](fields map[string]T) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func expectedType(out any) string {
	t := reflect.TypeOf(out)
	if t == nil {
		return "non_null"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int64:
		return "integer"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "array_of_objects"
	}
}

// detailViews also updates field params after input paths acquire their parents.
func detailViews(details []policy.Detail) []policy.Detail {
	out := make([]policy.Detail, 0, len(details))
	for _, d := range details {
		out = append(out, policy.NewDetail(d.Path, d.Code, d.Message, d.Params))
	}
	return out
}

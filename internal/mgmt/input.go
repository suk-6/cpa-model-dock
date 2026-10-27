package mgmt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
)

type object map[string]json.RawMessage

const maxBody = 4 << 20

func decodeObject(body []byte) (object, []policy.Detail) {
	if len(body) > maxBody {
		return nil, []policy.Detail{detail("body", "body_too_large", "Management request is too large")}
	}
	var obj object
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
		return []policy.Detail{detail(name, "invalid_type", "Invalid field value")}
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
			details = append(details, detail(path, "invalid_type", "Field must not be null"))
		case map[string]any:
			for key, child := range value {
				walk(child, path+"."+key)
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
		return 0, []policy.Detail{detail("revision", "invalid_revision", "Revision must be a nonnegative integer")}
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
			return 0, []policy.Detail{detail("revision", "invalid_revision", "If-Match must contain one revision")}
		}
	}
	if !hasBody && !hasHeader {
		return 0, []policy.Detail{detail("revision", "required", "A revision or If-Match header is required")}
	}
	if hasBody && hasHeader && bodyRevision != headerRevision {
		return 0, []policy.Detail{detail("revision", "invalid_revision", "Revision and If-Match disagree")}
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
			details = append(details, detail(key, "required", "Field is required for a full state replacement"))
		}
	}
	base := policy.State{Schema: current.Schema, Revision: current.Revision, UpdatedAt: current.UpdatedAt}
	draft, patchDetails := draftState(obj, base)
	details = append(details, patchDetails...)
	if raw, ok := obj["schema"]; ok {
		var schema int
		if json.Unmarshal(raw, &schema) != nil || schema != current.Schema {
			details = append(details, detail("schema", "invalid_schema", "Unsupported state schema"))
		}
	}
	if len(details) == 0 {
		details = assignIDs(&draft, current, true)
	}
	return draft, details
}

func draftState(obj object, saved policy.State) (policy.State, []policy.Detail) {
	// StateCopy already detached saved's slices and pointers from the live snapshot.
	var details []policy.Detail
	details = append(details, field(obj, "settings", &saved.Settings)...)
	details = append(details, field(obj, "families", &saved.Families)...)
	details = append(details, field(obj, "aliases", &saved.Aliases)...)
	details = append(details, field(obj, "rules", &saved.Rules)...)
	return saved, details
}

func assignIDs(state *policy.State, current policy.State, persisted bool) []policy.Detail {
	known := map[string]map[string]bool{"families": {}, "aliases": {}, "rules": {}}
	for _, f := range current.Families {
		known["families"][f.ID] = true
	}
	for _, a := range current.Aliases {
		known["aliases"][a.ID] = true
	}
	for _, r := range current.Rules {
		known["rules"][r.ID] = true
	}
	var details []policy.Detail
	assign := func(id *string, kind, prefix string, index int) {
		path := kind + "[" + strconv.Itoa(index) + "].id"
		if *id != "" {
			if persisted && kind == "aliases" && !known[kind][*id] {
				details = append(details, detail(path, "server_generated_id", "Omit the ID for a new entry"))
			}
			return
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			details = append(details, detail(path, "id_unavailable", "Could not generate an entry ID"))
			return
		}
		*id = prefix + hex.EncodeToString(token[:])
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

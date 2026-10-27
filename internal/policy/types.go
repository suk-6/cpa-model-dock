package policy

import "time"

type Settings struct {
	WhitelistEnabled       bool `json:"whitelist_enabled"`
	RewriteResponseModel   bool `json:"rewrite_response_model"`
	RefreshIntervalSeconds int  `json:"refresh_interval_seconds"`
}
type Family struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Template   string `json:"template"`
	Provider   string `json:"provider"`
	AllowDated bool   `json:"allow_dated"`
}
type Auto struct {
	Family            string `json:"family"`
	IncludePrerelease bool   `json:"include_prerelease"`
	HealthCheck       bool   `json:"health_check"`
}
type Alias struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Enabled              bool   `json:"enabled"`
	Mode                 string `json:"mode"`
	Target               string `json:"target"`
	Provider             string `json:"provider"`
	ExposeOriginal       bool   `json:"expose_original"`
	RewriteResponseModel *bool  `json:"rewrite_response_model"`
	Auto                 Auto   `json:"auto"`
}
type Rule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Note    string `json:"note,omitempty"`
}
type State struct {
	Schema    int       `json:"schema"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
	Settings  Settings  `json:"settings"`
	Families  []Family  `json:"families"`
	Aliases   []Alias   `json:"aliases"`
	Rules     []Rule    `json:"rules"`
}
type Model struct {
	ID              string   `json:"id"`
	Providers       []string `json:"providers"`
	CredentialCount int      `json:"credential_count"`
	Routable        bool     `json:"routable"`
}

// Catalog contains only sanitized registration facts, never credential secrets.
type Catalog struct {
	// Generation orders completed refresh attempts, including failures; never a policy revision.
	Generation  uint64    `json:"-"`
	Models      []Model   `json:"models"`
	Prefixes    []string  `json:"-"`
	RefreshedAt time.Time `json:"refreshed_at"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
}
type Detail struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Decision struct {
	Allowed        bool     `json:"allowed"`
	Status         int      `json:"status"`
	Code           string   `json:"code"`
	Reason         string   `json:"reason"`
	Steps          []string `json:"steps"`
	ResolvedTarget string   `json:"resolved_target,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	Alias          *Alias   `json:"-"`
}

// Snapshot owns copies of all state and catalog data and is immutable after construction.
type Snapshot struct {
	// Exported views are detached copies for compatibility. Runtime decisions use
	// only the private data captured by New; callers must construct snapshots with New.
	State       State
	Catalog     Catalog
	Valid       bool
	state       State
	catalog     Catalog
	valid       bool
	initialized bool
}

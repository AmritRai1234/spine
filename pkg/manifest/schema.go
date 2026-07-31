package manifest

// SpineSchema holds the full parsed manifest.
type SpineSchema struct {
	SpineVersion int            `json:"spine_version"`
	Tenant       string         `json:"tenant,omitempty"` // Multi-tenancy isolation (Year 3 feature)
	Includes     []string       `json:"includes,omitempty"`
	DbTables     []string       `json:"db_tables"`
	Database     DatabaseConfig `json:"database,omitempty"`
	Access       []AccessRule   `json:"access,omitempty"`
	Nodes        []Node         `json:"nodes"`
	Routes       []Route        `json:"routes"`
}

// AccessRule defines a role-based access policy with optional row-level filtering.
// When no Access rules are defined, the engine falls back to single APIKey auth.
type AccessRule struct {
	Role     string   `json:"role"`
	Key      string   `json:"-"`        // Never serialized — resolved from manifest or env var
	Tenant   string   `json:"tenant,omitempty"`   // Tenant ID scoping
	ReadOnly bool     `json:"read_only,omitempty"`
	Filter   string   `json:"filter,omitempty"`   // WHERE clause injected on table queries
	Events   []string `json:"events,omitempty"`   // Whitelist of emittable events (nil = all)
}

// OutboxConfig holds configuration for durable outbox worker pool retries.
type OutboxConfig struct {
	MaxWorkers int `json:"max_workers,omitempty"`
	MaxRetries int `json:"max_retries,omitempty"`
	BackoffMs  int `json:"backoff_ms,omitempty"`
}

// DatabaseConfig holds database configuration including table definitions and outbox settings.
type DatabaseConfig struct {
	Tables []string     `json:"tables,omitempty"`
	Outbox OutboxConfig `json:"outbox,omitempty"`
}

// Node represents a UI page or backend service declared in the manifest.
type Node struct {
	Name      string   `json:"name"`
	OwnsFiles []string `json:"owns_files,omitempty"`
	Emits     []Emit   `json:"emits,omitempty"`
	Listens   []Listen `json:"listens,omitempty"`
}

// Emit declares an event a node can fire, with typed payload fields.
type Emit struct {
	Event  string         `json:"event"`
	Fields []PayloadField `json:"fields,omitempty"`
}

// Listen declares a state a node subscribes to.
type Listen struct {
	State  string         `json:"state"`
	Fields []PayloadField `json:"fields,omitempty"`
}

// PayloadField is a named, typed field within an event payload.
type PayloadField struct {
	Name      string `json:"name"`
	FieldType string `json:"field_type"`
}

// Route maps an event to a sequence of steps and an optional state emission.
type Route struct {
	OnEvent     string      `json:"on_event"`
	Cron        string      `json:"cron,omitempty"` // Scheduled route execution (Year 5 feature)
	IfCondition string      `json:"if_condition,omitempty"`
	Parallel    bool        `json:"parallel,omitempty"`
	Steps       []RouteStep `json:"steps"`
	EmitState   string      `json:"emit_state,omitempty"`
	OnFailure   string      `json:"on_failure,omitempty"`
}

// RouteStep is a single action within a route (e.g. db.insert).
type RouteStep struct {
	Action      string            `json:"action"`
	IfCondition string            `json:"if_condition,omitempty"`
	Table       string            `json:"table,omitempty"`
	Input       string            `json:"input,omitempty"`
	URL         string            `json:"url,omitempty"`
	Message     string            `json:"message,omitempty"`
	Where       string            `json:"where,omitempty"`
	MaxAttempts int               `json:"max_attempts,omitempty"`
	BackoffMs   int               `json:"backoff_ms,omitempty"`
	OnFailure   string            `json:"on_failure,omitempty"`
	Compensate  string            `json:"compensate,omitempty"` // Action to run on saga rollback (e.g. db.delete, http.post)
	Config      map[string]string `json:"config,omitempty"`
}

package manifest

// SpineSchema holds the full parsed manifest.
type SpineSchema struct {
	SpineVersion int      `json:"spine_version"`
	Includes     []string `json:"includes,omitempty"`
	DbTables     []string `json:"db_tables"`
	Nodes        []Node   `json:"nodes"`
	Routes       []Route  `json:"routes"`
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
	IfCondition string      `json:"if_condition,omitempty"`
	Parallel    bool        `json:"parallel,omitempty"`
	Steps       []RouteStep `json:"steps"`
	EmitState   string      `json:"emit_state,omitempty"`
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
	Config      map[string]string `json:"config,omitempty"`
}

package sieve

type Rule struct {
    Part  string `yaml:"part"`
    Match string `yaml:"match"`
    Val   string `yaml:"val"`
    Opt   string `yaml:"opt"`
}

type Action struct {
    Action string `yaml:"action"`
    Dest   string `yaml:"dest"`
}

type FilterEntry struct {
    Filtername string   `yaml:"filtername"`
    Enabled    int      `yaml:"enabled"`
    Rules      []Rule   `yaml:"rules"`
    Actions    []Action `yaml:"actions"`
    Unescaped  int      `yaml:"unescaped,omitempty"`
}

type Filter struct {
    Filter  []FilterEntry `yaml:"filter"`
    Version string        `yaml:"version"`
}

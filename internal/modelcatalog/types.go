package modelcatalog

type Target struct {
	Provider  string
	Binary    string
	ConfigDir string
	Env       []string
}

type Model struct {
	ID               string   `json:"id"`
	ResolvedModel    string   `json:"resolvedModel,omitempty"`
	DisplayName      string   `json:"displayName,omitempty"`
	SupportsEffort   *bool    `json:"supportsEffort,omitempty"`
	SupportedEfforts []string `json:"supportedEfforts"`
	DefaultEffort    string   `json:"defaultEffort,omitempty"`
	Hidden           bool     `json:"hidden,omitempty"`
}

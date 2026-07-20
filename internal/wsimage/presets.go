package wsimage

// Preset is an insertable toolchain snippet (DESIGN2 §8.2): the UI appends
// Lines to the project's fragment and Domains to workspace.extra_domains —
// visible and editable afterwards, never a hidden dependency.
type Preset struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Lines   []string `json:"lines"`
	Domains []string `json:"domains"`
}

// Presets are starter toolchains; exact contents are ordinary Dockerfile
// text the user owns after insertion.
func Presets() []Preset {
	return []Preset{
		{
			Name: "go", Label: "Go 1.24",
			Lines: []string{
				`RUN curl -fsSL "https://go.dev/dl/go1.24.5.linux-amd64.tar.gz" | tar -C /usr/local -xz`,
				`ENV PATH="/usr/local/go/bin:/home/factory/go/bin:${PATH}"`,
			},
			Domains: []string{"proxy.golang.org", "sum.golang.org", "storage.googleapis.com"},
		},
		{
			Name: "node", Label: "Node 22 build tools",
			// Node itself ships in the base (shim runtime); this adds the
			// native-module toolchain and the npm registry egress.
			Lines: []string{
				`RUN apt-get update && apt-get install -y --no-install-recommends build-essential python3 && rm -rf /var/lib/apt/lists/*`,
			},
			Domains: []string{"registry.npmjs.org"},
		},
		{
			Name: "python", Label: "Python 3 + pip",
			Lines: []string{
				`RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-pip python3-venv && rm -rf /var/lib/apt/lists/*`,
			},
			Domains: []string{"pypi.org", "files.pythonhosted.org"},
		},
		{
			Name: "jvm", Label: "OpenJDK 21 + Maven",
			Lines: []string{
				`RUN apt-get update && apt-get install -y --no-install-recommends openjdk-21-jdk-headless maven && rm -rf /var/lib/apt/lists/*`,
			},
			Domains: []string{"repo.maven.apache.org"},
		},
		{
			Name: "build-essential", Label: "C toolchain + make",
			Lines: []string{
				`RUN apt-get update && apt-get install -y --no-install-recommends build-essential make && rm -rf /var/lib/apt/lists/*`,
			},
			Domains: []string{},
		},
	}
}

package service

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Manifest is a task manifest (tech design 5.3). Its JSON shape is the
// Manifest schema of the API, and versions store it in that shape as
// submitted, without defaults: defaults are applied when a run is handed
// to a runner (RunSpec).
type Manifest struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Workflow    []WorkflowStep `json:"workflow,omitempty"`
	Runtime     RuntimeSpec    `json:"runtime"`
	Entrypoint  []string       `json:"entrypoint"`
	Params      map[string]any `json:"params,omitempty"`
	Stages      []StageSpec    `json:"stages,omitempty"`
	Limits      *Limits        `json:"limits,omitempty"`
	Network     string         `json:"network,omitempty"`
	Concurrency string         `json:"concurrency,omitempty"`
	// RunnerLabels are labels a runner must have to claim the task's runs
	// (tech design 4.4).
	RunnerLabels []string     `json:"runner_labels,omitempty"`
	Retry        *RetryPolicy `json:"retry,omitempty"`
	// Credentials are referenced by name; see credentials_manifest.go.
	Credentials []CredentialSpec `json:"credentials,omitempty"`
	// Tools (tech design 7.8) are stored as submitted. SideEffects is a
	// pointer so an explicit false stays apart from an omitted field, and
	// diffs show what was written.
	Tools       []string    `json:"tools,omitempty"`
	SideEffects *bool       `json:"side_effects,omitempty"`
	Panels      []PanelSpec `json:"panels,omitempty"`
}

// WorkflowStep is one operation in the task's versioned workflow diagram.
type WorkflowStep struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// PanelSpec declares an agent panel over the run result's data.
type PanelSpec struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	Title   string        `json:"title"`
	Source  string        `json:"source"`
	Unit    string        `json:"unit,omitempty"`
	History *int          `json:"history,omitempty"`
	Columns []PanelColumn `json:"columns,omitempty"`
}

// PanelColumn is a column of a table panel.
type PanelColumn struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// RuntimeSpec selects the runtime; the choices are listed by Runtimes.
type RuntimeSpec struct {
	Language string `json:"language"`
	Version  string `json:"version"`
	Lockfile string `json:"lockfile,omitempty"`
}

// StageSpec declares a stage the task reports with `sbx stage`.
type StageSpec struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	After []string `json:"after,omitempty"`
}

// Limits are resource limits; empty fields take the defaults.
type Limits struct {
	Timeout string   `json:"timeout,omitempty"`
	CPU     *float64 `json:"cpu,omitempty"`
	Memory  string   `json:"memory,omitempty"`
}

// Manifest values.
const (
	NetworkNone       = "none"
	NetworkFull       = "full"
	ConcurrencyForbid = "forbid"
	ConcurrencyAllow  = "allow"
)

// Runtime is an entry of the runtime catalogue that discover reports.
type Runtime struct {
	Language string
	Version  string
	// Lockfile is the dependency lock file the runtime installs from,
	// empty when it has none.
	Lockfile string
	// manifestFile is the package manifest that must accompany Lockfile.
	manifestFile string
}

// Runtime languages (tech design 7.3).
const (
	LanguagePython = "python"
	LanguageNode   = "node"
	LanguageShell  = "shell"
)

// runtimes is the runtime catalogue. Each runner has an image per
// language, so a language has exactly one version.
var runtimes = []Runtime{
	{Language: LanguagePython, Version: "3.12", Lockfile: "uv.lock", manifestFile: "pyproject.toml"},
	{Language: LanguageNode, Version: "22", Lockfile: "package-lock.json", manifestFile: "package.json"},
	{Language: LanguageShell, Version: "5"},
}

// Runtimes returns the runtime catalogue. A manifest's runtime must be one
// of these; Lockfile is optional in the manifest.
func Runtimes() []Runtime {
	return slices.Clone(runtimes)
}

// Manifest rules. Defaults match the defaults in the API schema.
const (
	defaultTimeout   = 10 * time.Minute
	maxTimeout       = 24 * time.Hour
	defaultMilliCPUs = 1000
	minCPU           = 0.1
	maxCPU           = 8
	mebibyte         = 1 << 20
	gibibyte         = 1 << 30
	defaultMemory    = 1 * gibibyte
	minMemory        = 64 * mebibyte
	maxMemory        = 16 * gibibyte
	maxParamsBytes   = 16 << 10
	maxTaskTitle     = 120
	maxDescription   = 500
	maxWorkflowSteps = 20
	maxWorkflowTitle = 80
	maxWorkflowText  = 300
	maxEntrypoint    = 32
	maxArgBytes      = 1024
	maxStages        = 50
	maxStageName     = 100
)

var (
	// stageIDPattern matches the StageSpec.id schema.
	stageIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	timeoutPattern = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h)$`)
	memoryPattern  = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`)
)

// validateManifest checks m against the manifest rules. paths is the set
// of submitted file paths, needed for the lock file rule.
func validateManifest(v *validation, m *Manifest, paths map[string]bool) {
	const at = "#/manifest"
	if m.Title != strings.TrimSpace(m.Title) || !utf8.ValidString(m.Title) ||
		utf8.RuneCountInString(m.Title) > maxTaskTitle ||
		strings.ContainsFunc(m.Title, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
		v.content(at+"/title", fmt.Sprintf("任务标题最多 %d 个字符，不能有首尾空白、控制字符或换行", maxTaskTitle))
	}
	if utf8.RuneCountInString(m.Description) > maxDescription {
		v.content(at+"/description", fmt.Sprintf("用途说明最多 %d 个字符", maxDescription))
	}
	validateWorkflow(v, m.Workflow)

	validateRuntime(v, at+"/runtime", m.Runtime, paths)

	switch {
	case len(m.Entrypoint) == 0:
		v.content(at+"/entrypoint", "入口不能为空，例如 [\"python\", \"main.py\"]")
	case len(m.Entrypoint) > maxEntrypoint:
		v.content(at+"/entrypoint", fmt.Sprintf("入口最多 %d 项", maxEntrypoint))
	}
	for i, arg := range m.Entrypoint {
		switch {
		case arg == "":
			v.content(fmt.Sprintf("%s/entrypoint/%d", at, i), "入口的每一项都不能为空")
		case len(arg) > maxArgBytes:
			v.content(fmt.Sprintf("%s/entrypoint/%d", at, i), fmt.Sprintf("入口的每一项最多 %d 字节", maxArgBytes))
		case strings.ContainsRune(arg, 0):
			v.content(fmt.Sprintf("%s/entrypoint/%d", at, i), "入口不能包含 NUL 字符")
		}
	}

	if m.Params != nil {
		if b, err := json.Marshal(m.Params); err != nil || len(b) > maxParamsBytes {
			v.content(at+"/params", "参数序列化为 JSON 后不能超过 16 KiB")
		}
	}

	validateStages(v, m.Stages)
	validateCredentialSpecs(v, m.Credentials)

	if l := m.Limits; l != nil {
		if l.Timeout != "" {
			if d, ok := parseTimeout(l.Timeout); !ok {
				v.content(at+"/limits/timeout", "执行时限的格式为正整数加单位 s、m 或 h，例如 10m")
			} else if d > maxTimeout {
				v.content(at+"/limits/timeout", "执行时限最长 24h")
			}
		}
		if l.CPU != nil && (math.IsNaN(*l.CPU) || *l.CPU < minCPU || *l.CPU > maxCPU) {
			v.content(at+"/limits/cpu", "CPU 上限为 0.1 到 8 核")
		}
		if l.Memory != "" {
			if b, ok := parseMemory(l.Memory); !ok {
				v.content(at+"/limits/memory", "内存上限的格式为正整数加单位 Mi 或 Gi，例如 512Mi")
			} else if b < minMemory || b > maxMemory {
				v.content(at+"/limits/memory", "内存上限为 64Mi 到 16Gi")
			}
		}
	}

	switch m.Network {
	case "", NetworkNone, NetworkFull:
	default:
		v.content(at+"/network", "网络策略只能是 none 或 full")
	}
	switch m.Concurrency {
	case "", ConcurrencyForbid, ConcurrencyAllow:
	default:
		v.content(at+"/concurrency", "并发策略只能是 forbid 或 allow")
	}
	if len(m.RunnerLabels) > runproto.MaxLabels {
		v.content(at+"/runner_labels", fmt.Sprintf("runner 标签最多 %d 个", runproto.MaxLabels))
	}
	for i, l := range m.RunnerLabels {
		if !runproto.ValidLabel(l) {
			v.content(fmt.Sprintf("%s/runner_labels/%d", at, i),
				fmt.Sprintf("runner 标签为 1 到 %d 个小写字母、数字、'-'、'_' 或 '.'，以字母或数字开头", runproto.MaxLabelLen))
		}
	}
	validateRetry(v, at+"/retry", m.Retry)
	validateReferences(v, m)
}

// validateWorkflow accepts an omitted workflow in older task bundles.
// Apply separately requires a workflow for every new submission.
func validateWorkflow(v *validation, steps []WorkflowStep) {
	const at = "#/manifest/workflow"
	if steps == nil {
		return
	}
	if len(steps) == 0 || len(steps) > maxWorkflowSteps {
		v.content(at, fmt.Sprintf("流程需要 1 到 %d 个步骤", maxWorkflowSteps))
		return
	}
	for i, step := range steps {
		if !validWorkflowText(step.Title, maxWorkflowTitle) ||
			strings.ContainsFunc(step.Title, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
			v.content(fmt.Sprintf("%s/%d/title", at, i), fmt.Sprintf("步骤标题需要 1 到 %d 个字符，不能换行或包含控制字符", maxWorkflowTitle))
		}
		if !validWorkflowText(step.Description, maxWorkflowText) ||
			strings.ContainsFunc(step.Description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) {
			v.content(fmt.Sprintf("%s/%d/description", at, i), fmt.Sprintf("用 1 到 %d 个字符说明这一步做什么", maxWorkflowText))
		}
	}
}

func validWorkflowText(s string, limit int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit && strings.TrimSpace(s) != ""
}

// validateReferences checks the rules of credentials and panels that the
// schema cannot express: names unique within the manifest.
func validateReferences(v *validation, m *Manifest) {
	const at = "#/manifest"
	names := map[string]bool{}
	for i, c := range m.Credentials {
		if names[c.Name] {
			v.content(fmt.Sprintf("%s/credentials/%d/name", at, i), fmt.Sprintf("凭证引用名 %s 重复", c.Name))
		}
		names[c.Name] = true
	}
	panels := map[string]bool{}
	for i, p := range m.Panels {
		if panels[p.ID] {
			v.content(fmt.Sprintf("%s/panels/%d/id", at, i), fmt.Sprintf("面板 ID %s 重复", p.ID))
		}
		panels[p.ID] = true
	}
}

// validateStages checks stage IDs, names and the after graph: every
// reference names another declared stage, and the references form no
// cycle.
func validateStages(v *validation, stages []StageSpec) {
	const at = "#/manifest/stages"
	if len(stages) > maxStages {
		v.content(at, fmt.Sprintf("最多声明 %d 个阶段", maxStages))
		return
	}
	declared := map[string]bool{}
	for i, s := range stages {
		switch {
		case !stageIDPattern.MatchString(s.ID):
			v.content(fmt.Sprintf("%s/%d/id", at, i), "阶段 ID 为 1 到 63 个字符，只能包含小写字母、数字、下划线和连字符，以字母或数字开头")
		case declared[s.ID]:
			v.content(fmt.Sprintf("%s/%d/id", at, i), fmt.Sprintf("阶段 ID %s 重复", s.ID))
		}
		declared[s.ID] = true
		if n := utf8.RuneCountInString(s.Name); n == 0 || n > maxStageName {
			v.content(fmt.Sprintf("%s/%d/name", at, i), fmt.Sprintf("阶段名为 1 到 %d 个字符", maxStageName))
		}
	}

	graphOK := true
	for i, s := range stages {
		seen := map[string]bool{}
		for j, dep := range s.After {
			where := fmt.Sprintf("%s/%d/after/%d", at, i, j)
			switch {
			case dep == s.ID:
				v.content(where, "阶段不能依赖自己")
				graphOK = false
			case !declared[dep]:
				v.content(where, fmt.Sprintf("依赖的阶段 %s 没有声明", dep))
				graphOK = false
			case seen[dep]:
				v.content(where, fmt.Sprintf("依赖的阶段 %s 重复", dep))
			}
			seen[dep] = true
		}
	}
	if !graphOK || hasStageIDErrors(v) {
		// With broken references or duplicate IDs the graph is not well
		// defined, and a cycle report would only add noise.
		return
	}
	if cycle := findCycle(stages); cycle != nil {
		v.content(at, "阶段的 after 依赖成环："+strings.Join(cycle, " → "))
	}
}

func hasStageIDErrors(v *validation) bool {
	return slices.ContainsFunc(v.fields, func(f FieldError) bool {
		return strings.HasPrefix(f.Pointer, "#/manifest/stages/") && strings.HasSuffix(f.Pointer, "/id")
	})
}

// findCycle returns the stages of a dependency cycle, first stage
// repeated at the end, or nil when the graph is acyclic. Every reference
// must name a declared stage.
func findCycle(stages []StageSpec) []string {
	after := map[string][]string{}
	for _, s := range stages {
		after[s.ID] = s.After
	}
	const (
		unvisited = iota
		inProgress
		finished
	)
	state := map[string]int{}
	var path []string
	var visit func(id string) []string
	visit = func(id string) []string {
		state[id] = inProgress
		path = append(path, id)
		for _, dep := range after[id] {
			switch state[dep] {
			case inProgress:
				start := slices.Index(path, dep)
				return append(slices.Clone(path[start:]), dep)
			case unvisited:
				if c := visit(dep); c != nil {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		state[id] = finished
		return nil
	}
	for _, s := range stages {
		if state[s.ID] == unvisited {
			if c := visit(s.ID); c != nil {
				return c
			}
		}
	}
	return nil
}

// parseTimeout parses a limits.timeout value such as 10m.
func parseTimeout(s string) (time.Duration, bool) {
	m := timeoutPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour}[m[2]]
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > int64(math.MaxInt64/unit) {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

// parseMemory parses a limits.memory value such as 512Mi into bytes.
func parseMemory(s string) (int64, bool) {
	m := memoryPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	unit := int64(mebibyte)
	if m[2] == "Gi" {
		unit = gibibyte
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > math.MaxInt64/unit {
		return 0, false
	}
	return n * unit, true
}

// validateRuntime checks the runtime against the catalogue, and that a
// lock file comes with its package manifest.
func validateRuntime(v *validation, at string, rt RuntimeSpec, paths map[string]bool) {
	i := slices.IndexFunc(runtimes, func(r Runtime) bool { return r.Language == rt.Language })
	if i < 0 {
		v.content(at+"/language", "运行时只支持 python、node 和 shell，见 discover 返回的 runtimes")
		return
	}
	def := runtimes[i]
	if rt.Version != def.Version {
		v.content(at+"/version", fmt.Sprintf("%s 只支持 %s，见 discover 返回的 runtimes", def.Language, def.Version))
	}
	switch {
	case rt.Lockfile == "":
	case def.Lockfile == "":
		v.content(at+"/lockfile", fmt.Sprintf("%s 运行时不安装依赖，不能设置锁文件", def.Language))
	case rt.Lockfile != def.Lockfile:
		v.content(at+"/lockfile", fmt.Sprintf("%s 运行时的锁文件只支持 %s", def.Language, def.Lockfile))
	case !paths[def.Lockfile] || !paths[def.manifestFile]:
		v.content("#/files", fmt.Sprintf("runtime.lockfile 为 %s 时，文件中必须同时有 %s 和 %s", def.Lockfile, def.Lockfile, def.manifestFile))
	}
}

// RunSpec returns the manifest as the runner needs it, with defaults
// applied. It fails only for a manifest that never passed validation.
func (m Manifest) RunSpec() (runproto.Manifest, error) {
	spec := runproto.Manifest{
		Runtime: runproto.Runtime{
			Language: m.Runtime.Language,
			Version:  m.Runtime.Version,
			Lockfile: m.Runtime.Lockfile,
		},
		Entrypoint:  slices.Clone(m.Entrypoint),
		Timeout:     defaultTimeout,
		MilliCPUs:   defaultMilliCPUs,
		MemoryBytes: defaultMemory,
		Network:     m.Network,
	}
	if spec.Network == "" {
		spec.Network = NetworkFull
	}
	for _, s := range m.Stages {
		spec.Stages = append(spec.Stages, runproto.Stage{ID: s.ID, Name: s.Name, After: slices.Clone(s.After)})
	}
	spec.Credentials = credentialRefs(m.Credentials)
	if l := m.Limits; l != nil {
		if l.Timeout != "" {
			d, ok := parseTimeout(l.Timeout)
			if !ok {
				return runproto.Manifest{}, fmt.Errorf("manifest: invalid timeout %q", l.Timeout)
			}
			spec.Timeout = d
		}
		if l.CPU != nil {
			spec.MilliCPUs = int64(math.Round(*l.CPU * 1000))
		}
		if l.Memory != "" {
			b, ok := parseMemory(l.Memory)
			if !ok {
				return runproto.Manifest{}, fmt.Errorf("manifest: invalid memory %q", l.Memory)
			}
			spec.MemoryBytes = b
		}
	}
	if len(spec.Entrypoint) == 0 {
		return runproto.Manifest{}, fmt.Errorf("manifest: empty entrypoint")
	}
	return spec, nil
}

// concurrency returns the effective concurrency policy.
func (m Manifest) concurrency() string {
	if m.Concurrency == "" {
		return ConcurrencyForbid
	}
	return m.Concurrency
}

// decodeManifest reads a manifest stored in a task version.
func decodeManifest(s string) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return Manifest{}, fmt.Errorf("decode stored manifest: %w", err)
	}
	return m, nil
}

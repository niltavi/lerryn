package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The configuration reference in docs/zh-CN/deploy.md and the example file in
// deploy/ are checked against the Config struct here, so that a key added,
// renamed or given a new default cannot leave the documentation behind.

const repoRoot = "../../.."

// key is one leaf setting of Config.
type key struct {
	path string // YAML path, e.g. runner.data_dir
	env  string // environment variable, e.g. LERRYN_RUNNER_DATA_DIR
	def  string // default as written in the reference table
}

// emptyDefault is how the reference table writes an empty default.
const emptyDefault = "（空）"

// configKeys walks Config and returns every leaf with its YAML path, its
// environment variable and its default from Default().
func configKeys(t *testing.T) []key {
	t.Helper()
	var keys []key
	var walk func(v reflect.Value, path, env string)
	walk = func(v reflect.Value, path, env string) {
		typ := v.Type()
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "" || name == "-" {
				t.Fatalf("field %s.%s has no yaml tag", typ.Name(), f.Name)
			}
			p := name
			if path != "" {
				p = path + "." + name
			}
			if f.Type.Kind() == reflect.Struct {
				prefix, ok := f.Tag.Lookup("envPrefix")
				if !ok {
					t.Fatalf("struct field %s has no envPrefix tag", p)
				}
				walk(v.Field(i), p, env+prefix)
				continue
			}
			e, ok := f.Tag.Lookup("env")
			if !ok {
				t.Fatalf("field %s has no env tag", p)
			}
			keys = append(keys, key{path: p, env: env + e, def: formatDefault(v.Field(i))})
		}
	}
	walk(reflect.ValueOf(Default()), "", EnvPrefix)
	return keys
}

func formatDefault(v reflect.Value) string {
	var s string
	switch v.Kind() {
	case reflect.String:
		s = v.String()
	case reflect.Bool:
		s = strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int64:
		s = strconv.FormatInt(v.Int(), 10)
	case reflect.Slice:
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = fmt.Sprint(v.Index(i).Interface())
		}
		s = strings.Join(parts, ",")
	default:
		panic(fmt.Sprintf("formatDefault: unsupported kind %s", v.Kind()))
	}
	if s == "" {
		return emptyDefault
	}
	return s
}

func TestConfigReferenceMatchesDocs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "zh-CN", "deploy.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(data), "<!-- config-reference:start -->")
	table, _, ok2 := strings.Cut(table, "<!-- config-reference:end -->")
	if !ok || !ok2 {
		t.Fatal("docs/zh-CN/deploy.md lacks the config-reference markers")
	}
	documented := map[string]key{}
	for line := range strings.Lines(table) {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[0]), "`") {
			continue // header, separator or blank line
		}
		k := key{path: unquote(cells[0]), env: unquote(cells[1]), def: unquote(cells[2])}
		if _, dup := documented[k.path]; dup {
			t.Errorf("docs/zh-CN/deploy.md lists %s twice", k.path)
		}
		documented[k.path] = k
	}

	for _, want := range configKeys(t) {
		got, ok := documented[want.path]
		if !ok {
			t.Errorf("docs/zh-CN/deploy.md: configuration reference lacks %s; add a row: | `%s` | `%s` | %s | ... |",
				want.path, want.path, want.env, quoteDefault(want.def))
			continue
		}
		if got.env != want.env {
			t.Errorf("docs/zh-CN/deploy.md: %s environment variable is %s, want %s", want.path, got.env, want.env)
		}
		if got.def != want.def {
			t.Errorf("docs/zh-CN/deploy.md: %s default is %q, want %q", want.path, got.def, want.def)
		}
		delete(documented, want.path)
	}
	for path := range documented {
		t.Errorf("docs/zh-CN/deploy.md documents %s, which Config does not have", path)
	}
}

func TestExampleFileCoversEveryKey(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "lerryn.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var inFile []string
	var flatten func(m map[string]any, prefix string)
	flatten = func(m map[string]any, prefix string) {
		for k, v := range m {
			p := prefix + k
			if sub, ok := v.(map[string]any); ok {
				flatten(sub, p+".")
				continue
			}
			inFile = append(inFile, p)
		}
	}
	flatten(doc, "")
	for _, k := range configKeys(t) {
		if !slices.Contains(inFile, k.path) {
			t.Errorf("deploy/lerryn.example.yaml lacks %s", k.path)
		}
	}
}

// TestDeployConfigsLoad keeps the configuration files shipped with the
// Compose topologies loadable.
func TestDeployConfigsLoad(t *testing.T) {
	for _, p := range []string{
		filepath.Join(repoRoot, "deploy", "single-node", "lerryn.yaml"),
		filepath.Join(repoRoot, "deploy", "multi-node", "server.yaml"),
		filepath.Join(repoRoot, "deploy", "multi-node", "runner.yaml"),
	} {
		if _, err := Load(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	server, err := Load(filepath.Join(repoRoot, "deploy", "multi-node", "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Behind Caddy, rate limits and audit records need the client address
	// from X-Forwarded-For, which only trusted_proxies unlocks.
	if server.Storage.Driver != StorageS3 || server.Database.Driver != DatabasePostgres || server.Server.RunnerRegistrationTokenFile == "" ||
		len(server.Server.TrustedProxies) == 0 {
		t.Errorf("multi-node server config: %+v", server)
	}
	runner, err := Load(filepath.Join(repoRoot, "deploy", "multi-node", "runner.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if runner.Runner.Mode != RunnerRemote || runner.Runner.ServerURL == "" || runner.Runner.RegistrationTokenFile == "" {
		t.Errorf("multi-node runner config: %+v", runner.Runner)
	}
}

func unquote(cell string) string {
	return strings.Trim(strings.TrimSpace(cell), "`")
}

func quoteDefault(def string) string {
	if def == emptyDefault {
		return def
	}
	return "`" + def + "`"
}

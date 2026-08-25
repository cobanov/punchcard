package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// .env.example is the only onboarding path a self-hoster has, so a variable
// that exists in Config but not in the file is invisible: the server reads it,
// the operator never sets it, and the symptom shows up somewhere unrelated.
// That had drifted to 19 missing variables — every OAuth credential, the
// webhook secret-encryption key, the metrics bearer token and the trusted-proxy
// list — before this test existed.
//
// The reverse direction matters just as much: a variable documented here but no
// longer read is a lie that costs an operator an afternoon.
func TestEnvExampleDocumentsEveryVariable(t *testing.T) {
	declared := envTagsOf(reflect.TypeOf(Config{}))
	if len(declared) == 0 {
		t.Fatal("no env tags found on Config; this test is not testing anything")
	}

	path := filepath.Join("..", "..", ".env.example")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	assign := regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)=`)
	documented := map[string]bool{}
	for _, m := range assign.FindAllStringSubmatch(string(raw), -1) {
		documented[m[1]] = true
	}

	var missing []string
	for _, name := range declared {
		if !documented[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("read by Config but absent from .env.example:\n\t%s",
			strings.Join(missing, "\n\t"))
	}

	declaredSet := map[string]bool{}
	for _, n := range declared {
		declaredSet[n] = true
	}
	var stale []string
	for name := range documented {
		if !declaredSet[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("documented in .env.example but not read by Config:\n\t%s",
			strings.Join(stale, "\n\t"))
	}
}

// envTagsOf collects every `env:"NAME"` tag on a struct, descending into
// embedded and nested structs so a future regrouping of Config keeps working.
func envTagsOf(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		// The tag carries options after the name, e.g. `env:"DATABASE_URL,required"`.
		if tag := f.Tag.Get("env"); tag != "" {
			if name, _, _ := strings.Cut(tag, ","); name != "" {
				out = append(out, name)
			}
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, envTagsOf(f.Type)...)
		}
	}
	return out
}

package httpapi

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every error code the service can send must be listed in the OpenAPI Error
// schema, so generated clients can decode every error. The contract tests only
// see the codes the integration tests happen to trigger; this scans the source.
func TestErrorCodesAreDocumented(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	enum := regexp.MustCompile(`(?s)            code:\n              type: "string"\n              enum:\n((?:                - "[a-z_]+"\n)+)`).FindSubmatch(spec)
	if enum == nil {
		t.Fatal("Error.code enum not found in api/openapi.yaml")
	}
	for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(enum[1], -1) {
		documented[string(m[1])] = true
	}

	emitted := map[string]string{}
	call := regexp.MustCompile(`(?:writeError|httpjson\.Error)\([^,]+, [^,]+, "([a-z_]+)"`)
	notFound := regexp.MustCompile(`h\.fail\(w, r, [^,]+, "([a-z]+)"\)`)
	for _, dir := range []string{".", "../authn/local", "../httpjson"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range call.FindAllSubmatch(src, -1) {
				emitted[string(m[1])] = file
			}
			// fail() turns ErrNotFound into "<resource>_not_found".
			for _, m := range notFound.FindAllSubmatch(src, -1) {
				if resource := string(m[1]); resource != "request" {
					emitted[resource+"_not_found"] = file
				}
			}
		}
	}
	if len(emitted) < 20 {
		t.Fatalf("found only %d error codes; the scan is broken", len(emitted))
	}
	var missing []string
	for code, file := range emitted {
		if !documented[code] {
			missing = append(missing, code+" ("+file+")")
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Fatalf("error codes missing from the Error schema in api/openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
}

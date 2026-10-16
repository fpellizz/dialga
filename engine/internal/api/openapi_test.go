package api

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

type openAPIDoc struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]struct {
			Required   []string       `yaml:"required"`
			Properties map[string]any `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func loadSpec(t *testing.T) openAPIDoc {
	t.Helper()
	var doc openAPIDoc
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	return doc
}

func TestOpenAPIRoutesMatchTheServer(t *testing.T) {
	s, err := New(Options{Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	var served, documented []string
	for _, rt := range s.routes() {
		served = append(served, rt.method+" "+rt.path)
	}
	for path, ops := range loadSpec(t).Paths {
		for method := range ops {
			if slices.Contains([]string{"get", "post", "put", "patch", "delete"}, method) {
				documented = append(documented, strings.ToUpper(method)+" "+path)
			}
		}
	}
	slices.Sort(served)
	slices.Sort(documented)
	if !slices.Equal(served, documented) {
		t.Fatalf("routes and openapi.yaml differ\nserved:     %v\ndocumented: %v", served, documented)
	}
}

// jsonFields returns the JSON names of a struct and those without omitempty.
func jsonFields(typ reflect.Type) (all, required []string) {
	for f := range typ.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		all = append(all, name)
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}
	slices.Sort(all)
	slices.Sort(required)
	return all, required
}

func TestOpenAPISchemasMatchTheGoTypes(t *testing.T) {
	types := map[string][]any{
		"Health":        {Health{}},
		"TargetSummary": {TargetSummary{}},
		"TargetList":    {TargetList{}},
		"StepEvent":     {check.Event{}, core.Event{}},
		"Notice":        {Notice{}},
		"Done":          {Done{}},
		"Task":          {mssql.Task{}},
		"TaskList":      {TaskList{}},

		"UploadsRequest":   {UploadsRequest{}},
		"DownloadRequest":  {DownloadRequest{}},
		"TransferPlan":     {transfer.Batch{}},
		"Plan":             {core.Plan{}},
		"Action":           {core.Action{}},
		"TransferItem":     {transfer.Item{}},
		"Rejection":        {transfer.Rejection{}},
		"TransferProgress": {transfer.Progress{}},
		"TransferResult":   {transfer.Result{}},
		"TransferDone":     {TransferDone{}},
	}
	schemas := loadSpec(t).Components.Schemas
	for name, schema := range schemas {
		goTypes, ok := types[name]
		if !ok {
			t.Errorf("schema %s has no Go type in this test", name)
			continue
		}
		var props []string
		for p := range schema.Properties {
			props = append(props, p)
		}
		slices.Sort(props)
		required := slices.Clone(schema.Required)
		slices.Sort(required)
		for _, v := range goTypes {
			all, req := jsonFields(reflect.TypeOf(v))
			if !slices.Equal(props, all) {
				t.Errorf("%s (%T): properties %v, Go fields %v", name, v, props, all)
			}
			if !slices.Equal(required, req) {
				t.Errorf("%s (%T): required %v, Go fields without omitempty %v", name, v, required, req)
			}
		}
	}
	for name := range types {
		if _, ok := schemas[name]; !ok {
			t.Errorf("Go type for %s has no schema in openapi.yaml", name)
		}
	}
}

func TestOpenAPIEndpoint(t *testing.T) {
	doc := testConfig
	h, _ := newTestServer(t, &doc, nil)
	rec := do(h, http.MethodGet, "/api/v1/openapi.yaml")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/yaml" ||
		!strings.HasPrefix(rec.Body.String(), "openapi: 3.1.0") {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

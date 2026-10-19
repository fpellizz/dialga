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
	"github.com/decisyon/dialga/engine/internal/discover"
	"github.com/decisyon/dialga/engine/internal/inspect"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
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
		"Health":          {Health{}},
		"TargetSummary":   {TargetSummary{}},
		"PasswordInfo":    {PasswordInfo{}},
		"PasswordRequest": {PasswordRequest{}},
		"TargetList":      {TargetList{}},
		"StepEvent":       {check.Event{}, core.Event{}},
		"Notice":          {Notice{}},
		"Done":            {Done{}},
		"Task":            {mssql.Task{}},
		"TaskList":        {TaskList{}},
		"Database":        {mssql.Database{}},
		"DatabaseList":    {DatabaseList{}},
		"Overview":        {Overview{}},
		// inspect
		"InspectRequest": {InspectRequest{}},
		"InspectResult":  {inspect.Result{}},
		"InspectTarget":  {inspect.Target{}},
		"InspectBackup":  {inspect.Backup{}},
		"InspectHeader":  {inspect.Header{}},
		"InspectFile":    {inspect.DatabaseFile{}},
		"InspectFinding": {inspect.Finding{}},
		// native operations (ops.go)
		"PlanAction":     {core.Action{}},
		"Plan":           {core.Plan{}},
		"PlanRequest":    {PlanRequest{}},
		"ExecuteRequest": {ExecuteRequest{}},
		"PlanPreview":    {PlanPreview{}},
		"PlanDone":       {PlanDone{}},
		"JobRef":         {JobRef{}},
		"Replacement":    {ops.Replacement{}},
		"ReplaceOutcome": {core.ReplaceOutcome{}},
		"DatabaseInfo":   {mssql.DatabaseInfo{}},
		"DatabaseFile":   {mssql.DatabaseFile{}},
		"ObjectCount":    {mssql.ObjectCount{}},
		"DatabaseReport": {ops.Report{}},
		"Login":          {mssql.Login{}},
		"LoginList":      {LoginList{}},
		"DatabaseUser":   {mssql.DatabaseUser{}},
		"DatabaseAccess": {ops.AccessView{}},
		// First run and configuration (firstrun.go).
		"InstallHint":        {discover.InstallHint{}},
		"Prerequisite":       {discover.Prerequisite{}},
		"PrerequisiteList":   {PrerequisiteList{}},
		"AWSProfile":         {discover.Profile{}},
		"ProfileList":        {ProfileList{}},
		"DiscoveredInstance": {discover.Instance{}},
		"InstanceDiscovery":  {discover.Instances{}},
		"DiscoveredBucket":   {discover.Bucket{}},
		"BucketDiscovery":    {BucketList{}},
		"TargetInput":        {TargetInput{}},
		// Jobs (jobs.go)
		"JobList":          {JobList{}},
		"JobSummary":       {jobs.Summary{}},
		"Job":              {jobs.Job{}},
		"Refusal":          {jobs.Refusal{}},
		"JobItem":          {jobs.Item{}},
		"JobStep":          {jobs.StepInfo{}},
		"JobEvent":         {jobs.Event{}},
		"JobRequest":       {JobRequest{}},
		"JobRequestItem":   {JobRequestItem{}},
		"JobPlan":          {pipeline.Plan{}},
		"JobItemPlan":      {pipeline.ItemPlan{}},
		"JobRefused":       {JobRefused{}},
		"TransferProgress": {jobs.TransferProgress{}},
		"TaskProgress":     {jobs.TaskStatus{}},
		"MultipartUpload":  {jobs.UploadState{}},
		"JobChecksum":      {jobs.ChecksumInfo{}},
		"ResumeRequest":    {ResumeRequest{}},
		"TunnelState":      {TunnelState{}},
		"TunnelList":       {TunnelList{}},

		"UploadsRequest":        {UploadsRequest{}},
		"DownloadRequest":       {DownloadRequest{}},
		"TransferPlan":          {transfer.Batch{}},
		"TransferItem":          {transfer.Item{}},
		"Rejection":             {transfer.Rejection{}},
		"TransferProgressEvent": {transfer.Progress{}},
		"TransferResult":        {transfer.Result{}},
		"TransferDone":          {TransferDone{}},
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

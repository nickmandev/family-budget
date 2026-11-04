package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// contractSpec is the subset of the handwritten OpenAPI document that the
// drift checks need. It intentionally ignores most of the specification: the
// contract is reviewed as prose and schemas, while these tests assert that the
// route table, error mapping and envelope shape cannot silently diverge from
// it.
type contractSpec struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Title       string `yaml:"title"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
	} `yaml:"info"`
	Servers    []contractServer            `yaml:"servers"`
	Paths      map[string]contractPathItem `yaml:"paths"`
	Components struct {
		Schemas map[string]contractSchema `yaml:"schemas"`
	} `yaml:"components"`
	XErrorCodes map[string]int `yaml:"x-error-codes"`
}

type contractServer struct {
	URL string `yaml:"url"`
}

type contractPathItem struct {
	Servers    []contractServer    `yaml:"servers"`
	Parameters []contractParameter `yaml:"parameters"`
	Get        *contractOperation  `yaml:"get"`
	Post       *contractOperation  `yaml:"post"`
	Put        *contractOperation  `yaml:"put"`
	Patch      *contractOperation  `yaml:"patch"`
	Delete     *contractOperation  `yaml:"delete"`
}

func (p contractPathItem) operations() map[string]*contractOperation {
	operations := make(map[string]*contractOperation)
	for method, operation := range map[string]*contractOperation{
		http.MethodGet:    p.Get,
		http.MethodPost:   p.Post,
		http.MethodPut:    p.Put,
		http.MethodPatch:  p.Patch,
		http.MethodDelete: p.Delete,
	} {
		if operation != nil {
			operations[method] = operation
		}
	}
	return operations
}

type contractOperation struct {
	OperationID string              `yaml:"operationId"`
	Parameters  []contractParameter `yaml:"parameters"`
}

type contractParameter struct {
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
}

type contractSchema struct {
	Type       string                    `yaml:"type"`
	Format     string                    `yaml:"format"`
	Ref        string                    `yaml:"$ref"`
	Required   []string                  `yaml:"required"`
	Properties map[string]contractSchema `yaml:"properties"`
	Enum       []string                  `yaml:"enum"`
}

// loadContractSpec reads docs/openapi.yaml relative to this package directory.
func loadContractSpec(t *testing.T) *contractSpec {
	t.Helper()

	path := filepath.Join("..", "..", "..", "docs", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var spec contractSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if spec.OpenAPI == "" || spec.Info.Version == "" || len(spec.Paths) == 0 {
		t.Fatalf("%s is missing openapi/info/paths", path)
	}
	return &spec
}

// documentedOperations returns the full "METHOD /path" set built from the
// document's servers and path keys. Path-level servers override the default so
// the probe endpoints outside /api/v1 are represented accurately.
func documentedOperations(spec *contractSpec) (map[string]contractOperation, map[string]string) {
	operations := make(map[string]contractOperation)
	operationIDs := make(map[string]string)
	defaultBase := "/api/v1"
	if len(spec.Servers) > 0 && spec.Servers[0].URL != "" {
		defaultBase = spec.Servers[0].URL
	}

	for path, item := range spec.Paths {
		base := defaultBase
		if len(item.Servers) > 0 && item.Servers[0].URL != "" {
			base = item.Servers[0].URL
		}
		fullPath := joinServerPath(base, path)
		for method, operation := range item.operations() {
			key := method + " " + fullPath
			operations[key] = *operation
			if operation.OperationID != "" {
				operationIDs[operation.OperationID] = key
			}
		}
	}
	return operations, operationIDs
}

func joinServerPath(base, path string) string {
	if base == "" || base == "/" {
		return "/" + strings.TrimLeft(path, "/")
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func TestOpenAPIRouteCoverage(t *testing.T) {
	harness := newResourceServer(t)
	spec := loadContractSpec(t)
	documented, operationIDs := documentedOperations(spec)

	seen := make(map[string]bool, len(documented))
	for _, route := range harness.server.Routes() {
		seen[route] = true
		if _, ok := documented[route]; !ok {
			t.Errorf("registered route %q is missing from docs/openapi.yaml", route)
		}
	}
	for route := range documented {
		if !seen[route] {
			t.Errorf("docs/openapi.yaml documents %q but the server does not register it", route)
		}
	}
	if len(operationIDs) != len(documented) {
		t.Errorf("operationId values are missing or duplicated: %d ids for %d operations", len(operationIDs), len(documented))
	}
	if len(documented) == 0 {
		t.Fatal("no operations were documented")
	}
	t.Logf("documented %d operations across %d paths", len(documented), len(spec.Paths))
}

func TestOpenAPIErrorCodeMappingDrift(t *testing.T) {
	spec := loadContractSpec(t)

	detail, ok := spec.Components.Schemas["ErrorDetail"]
	if !ok {
		t.Fatal("ErrorDetail schema is missing")
	}
	detailCode := detail.Properties["code"]
	documentedCodes := make(map[string]bool, len(detailCode.Enum))
	for _, code := range detailCode.Enum {
		documentedCodes[code] = true
	}
	if len(documentedCodes) != len(spec.XErrorCodes) {
		t.Errorf("ErrorDetail enum has %d codes, x-error-codes has %d", len(documentedCodes), len(spec.XErrorCodes))
	}

	// codeMappings -> spec: every live code is documented with the same status.
	for code, mapping := range codeMappings {
		if !documentedCodes[code] {
			t.Errorf("codeMappings entry %s is not in the ErrorDetail enum", code)
		}
		status, ok := spec.XErrorCodes[code]
		if !ok {
			t.Errorf("codeMappings entry %s has no x-error-codes status", code)
			continue
		}
		if status != mapping.status {
			t.Errorf("code %s status drift: handlers use %d, spec documents %d", code, mapping.status, status)
		}
	}

	// spec -> codeMappings: no documented code is unknown to the handlers.
	for code, status := range spec.XErrorCodes {
		mapping, ok := codeMappings[code]
		if !ok {
			t.Errorf("documented code %s is not in codeMappings", code)
			continue
		}
		if mapping.status != status {
			t.Errorf("documented code %s status %d disagrees with handler status %d", code, status, mapping.status)
		}
		if !documentedCodes[code] {
			t.Errorf("x-error-codes entry %s is missing from the ErrorDetail enum", code)
		}
	}
}

func TestOpenAPIErrorEnvelopeShape(t *testing.T) {
	spec := loadContractSpec(t)
	detail, ok := spec.Components.Schemas["ErrorDetail"]
	if !ok {
		t.Fatal("ErrorDetail schema is missing")
	}

	schemaFields := sortedKeys(detail.Properties)
	if got := strings.Join(schemaFields, ","); got != "code,details,field,message,request_id" {
		t.Fatalf("ErrorDetail fields = %s, want code,details,field,message,request_id", got)
	}
	if got := strings.Join(sortedStrings(detail.Required), ","); got != strings.Join(schemaFields, ",") {
		t.Fatalf("ErrorDetail required = %v, want every field %v", detail.Required, schemaFields)
	}

	field := "email"
	envelope := ErrorEnvelope{Error: ErrorDetail{
		Code:      "INVALID_VALUE",
		Message:   "The request contains an invalid value.",
		Field:     &field,
		Details:   map[string]any{"reason": "bad"},
		RequestID: "req-1",
	}}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if got := sortedMapKeys(top); strings.Join(got, ",") != "error" {
		t.Fatalf("envelope top-level fields = %v, want [error]", got)
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(top["error"], &inner); err != nil {
		t.Fatalf("decode error detail: %v", err)
	}
	if got := sortedMapKeys(inner); strings.Join(got, ",") != strings.Join(schemaFields, ",") {
		t.Fatalf("marshaled error fields = %v, want %v", got, schemaFields)
	}
	if len(inner) != len(detail.Required) {
		t.Fatalf("marshaled error has %d fields, schema requires %d", len(inner), len(detail.Required))
	}
}

var pathParameterPattern = regexp.MustCompile(`\{[^}]+\}`)

const zeroUUID = "00000000-0000-4000-8000-000000000000"

// TestFinancialMutationsDocumentIdempotencyKey derives the financial mutation
// set from the live handlers: a POST route whose handler rejects a keyless
// request with an Idempotency-Key error is financial and must document the
// header. This keeps the test honest without a hardcoded route list.
func TestFinancialMutationsDocumentIdempotencyKey(t *testing.T) {
	harness := newResourceServer(t)
	spec := loadContractSpec(t)
	documented, _ := documentedOperations(spec)

	financial := 0
	for _, route := range harness.server.Routes() {
		method, pattern, ok := strings.Cut(route, " ")
		if !ok || method != http.MethodPost || route == "POST /api/v1/auth/logout" {
			// Logout invalidates the harness session; it is not a financial
			// mutation and must not disturb the remaining probes.
			continue
		}
		path := pathParameterPattern.ReplaceAllString(pattern, zeroUUID)
		response := harness.do(t, http.MethodPost, path, "{}", nil)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Idempotency-Key") {
			continue
		}
		financial++
		operation, ok := documented[route]
		if !ok {
			t.Errorf("financial mutation %s is missing from docs/openapi.yaml", route)
			continue
		}
		if !hasHeaderParameter(operation.Parameters, "Idempotency-Key") {
			t.Errorf("financial mutation %s does not document a required Idempotency-Key header", route)
		}
	}
	if financial == 0 {
		t.Fatal("no financial mutations were detected; the handler check is not exercising anything")
	}
	t.Logf("checked %d financial mutations for the Idempotency-Key header", financial)
}

func hasHeaderParameter(parameters []contractParameter, name string) bool {
	for _, parameter := range parameters {
		if parameter.In == "header" && parameter.Name == name && parameter.Required {
			return true
		}
	}
	return false
}

func TestOpenAPIDateSemantics(t *testing.T) {
	spec := loadContractSpec(t)

	effectiveDate, ok := spec.Components.Schemas["EffectiveDate"]
	if !ok {
		t.Fatal("EffectiveDate schema is missing")
	}
	if effectiveDate.Type != "string" || effectiveDate.Format != "date" {
		t.Fatalf("EffectiveDate = %+v, want string/date", effectiveDate)
	}
	timestamp, ok := spec.Components.Schemas["Timestamp"]
	if !ok {
		t.Fatal("Timestamp schema is missing")
	}
	if timestamp.Type != "string" || timestamp.Format != "date-time" {
		t.Fatalf("Timestamp = %+v, want string/date-time", timestamp)
	}

	financial, ok := spec.Components.Schemas["FinancialRequest"]
	if !ok {
		t.Fatal("FinancialRequest schema is missing")
	}
	if got := financial.Properties["effective_date"].Ref; got != "#/components/schemas/EffectiveDate" {
		t.Fatalf("effective_date ref = %q, want EffectiveDate", got)
	}

	for _, field := range []struct {
		schema string
		name   string
	}{
		{"Household", "created_at"},
		{"Household", "updated_at"},
		{"Account", "created_at"},
		{"User", "created_at"},
		{"Membership", "joined_at"},
		{"Transaction", "committed_at"},
	} {
		schema, ok := spec.Components.Schemas[field.schema]
		if !ok {
			t.Fatalf("%s schema is missing", field.schema)
		}
		if got := schema.Properties[field.name].Ref; got != "#/components/schemas/Timestamp" {
			t.Errorf("%s.%s ref = %q, want Timestamp", field.schema, field.name, got)
		}
	}
}

func TestOpenAPIDocumentsContractSemantics(t *testing.T) {
	spec := loadContractSpec(t)

	if spec.Info.Version != "1.0.0" {
		t.Errorf("info.version = %q, want 1.0.0", spec.Info.Version)
	}
	if len(spec.Servers) == 0 || spec.Servers[0].URL != "/api/v1" {
		t.Errorf("default server = %+v, want /api/v1", spec.Servers)
	}
	description := strings.Join(strings.Fields(strings.ToLower(spec.Info.Description)), " ")
	for _, term := range []string{
		"effective", "date-only", "idempotency", "cursor", "household",
		"not an active member", "draft", "reserved", "suggestion",
		"committed", "error envelope",
	} {
		if !strings.Contains(description, term) {
			t.Errorf("info.description does not document %q", term)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys(m map[string]json.RawMessage) []string {
	return sortedKeys(m)
}

func sortedStrings(values []string) []string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}

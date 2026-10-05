package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/logging"
)

// schemaPath is the published schema, relative to this package.
const schemaPath = "../../schema/sol.schema.json"

// schemaNode describes one pointer in the JSON Schema and the struct it must mirror. required
// lists the keys the start-up actually demands there; nil means "no required keys", which is
// itself an assertion (checked against the binary: a rule without an action is rejected, an
// action without name/type is rejected, an empty auth block is accepted and means bearer).
type schemaNode struct {
	pointer  string
	typ      reflect.Type
	required []string
}

// loadSchema reads and decodes the published schema.
func loadSchema(t *testing.T) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(schemaPath))
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	return schema
}

// schemaNodeAt resolves a "/properties/x/y" pointer inside the schema.
func schemaNodeAt(t *testing.T, schema map[string]any, pointer string) map[string]any {
	t.Helper()

	current := schema

	for step := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		if step == "" {
			continue
		}

		next, ok := current[step].(map[string]any)
		require.True(t, ok, "schema pointer %q: %q is not an object", pointer, step)

		current = next
	}

	return current
}

// schemaProperties lists the property names declared at a schema node.
func schemaProperties(t *testing.T, schema map[string]any, pointer string) []string {
	t.Helper()

	properties, hasProperties := schemaNodeAt(t, schema, pointer)["properties"].(map[string]any)
	require.True(t, hasProperties, "schema pointer %q has no properties", pointer)

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}

	return names
}

// schemaStrings reads a JSON array of strings at a schema node.
func schemaStrings(t *testing.T, node map[string]any, key string) []string {
	t.Helper()

	raw, ok := node[key].([]any)
	require.True(t, ok, "schema node has no %s array", key)

	values := make([]string, 0, len(raw))
	for _, item := range raw {
		value, isString := item.(string)
		require.True(t, isString, "schema %s array has a non-string value", key)

		values = append(values, value)
	}

	return values
}

// yamlTags lists the YAML keys the struct decodes from.
func yamlTags(t *testing.T, typ reflect.Type) []string {
	t.Helper()

	tags := make([]string, 0, typ.NumField())

	for field := range typ.Fields() {
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no yaml tag", typ.Name(), field.Name)
		}

		tags = append(tags, strings.Split(tag, ",")[0])
	}

	return tags
}

// enumValues reads the allowed values of a schema node, whether it spells them as an enum or as
// a single const.
func enumValues(t *testing.T, schema map[string]any, pointer string) []string {
	t.Helper()

	node := schemaNodeAt(t, schema, pointer)

	if constValue, ok := node["const"].(string); ok {
		return []string{constValue}
	}

	require.Contains(t, node, "enum", "schema pointer %q has neither enum nor const", pointer)

	return schemaStrings(t, node, "enum")
}

// schemaNodes lists every object the schema must mirror, with the struct behind it.
func schemaNodes() []schemaNode {
	return []schemaNode{
		{pointer: "", typ: reflect.TypeFor[fileConfig]()},
		{pointer: "/properties/server", typ: reflect.TypeFor[serverConfig]()},
		{pointer: "/properties/server/properties/http", typ: reflect.TypeFor[httpConfig]()},
		{pointer: "/properties/server/properties/http/properties/auth", typ: reflect.TypeFor[authConfig]()},
		{pointer: "/properties/server/properties/http/properties/tls", typ: reflect.TypeFor[tlsConfig]()},
		{pointer: "/properties/logging", typ: reflect.TypeFor[loggingConfig]()},
		{pointer: "/properties/security", typ: reflect.TypeFor[securityConfig]()},
		{
			pointer:  "/properties/security/properties/remote_command_auth",
			typ:      reflect.TypeFor[remoteAuthConfig](),
			required: []string{"type"},
		},
		{pointer: "/$defs/interfaceBlock", typ: reflect.TypeFor[ifaceConfig](), required: []string{"name"}},
		{pointer: "/$defs/action", typ: reflect.TypeFor[actionConfig](), required: []string{"name", "type"}},
		{
			pointer:  "/$defs/command",
			typ:      reflect.TypeFor[commandConfig](),
			required: []string{"id", "type", "command"},
		},
		{pointer: "/$defs/arg", typ: reflect.TypeFor[argConfig]()},
		{pointer: "/$defs/rule", typ: reflect.TypeFor[ruleConfig](), required: []string{"action"}},
		{pointer: "/$defs/match", typ: reflect.TypeFor[matchConfig]()},
		{pointer: "/$defs/macBlock", typ: reflect.TypeFor[macConfig](), required: []string{"kind"}},
		{pointer: "/$defs/content", typ: reflect.TypeFor[contentConfig](), required: []string{"kind"}},
	}
}

// TestSchemaMirrorsTheConfigStructs is the drift guard: the published schema must describe
// exactly the YAML keys the loader accepts, so a new configuration field cannot land without the
// editor completion that goes with it.
func TestSchemaMirrorsTheConfigStructs(t *testing.T) {
	schema := loadSchema(t)

	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])

	for _, node := range schemaNodes() {
		name := node.pointer
		if name == "" {
			name = "(root)"
		}

		t.Run(name, func(t *testing.T) {
			// Unknown keys are rejected by the loader (KnownFields), so the schema must reject
			// them too, or an editor would accept a config the binary refuses.
			require.Equal(t, false, schemaNodeAt(t, schema, node.pointer)["additionalProperties"],
				"the schema must forbid unknown keys where the loader does")

			require.ElementsMatch(t, yamlTags(t, node.typ), schemaProperties(t, schema, node.pointer))

			if node.required == nil {
				return
			}

			declared := schemaStrings(t, schemaNodeAt(t, schema, node.pointer), "required")
			require.ElementsMatch(t, node.required, declared)
		})
	}
}

// TestSchemaEnumsMatchTheDomain keeps the schema's value lists in step with the constants the
// loader accepts: a hand-written enum drifts silently. This check is what caught "exact" being
// listed as a content kind while the domain only knows any/none/suffix/prefix.
func TestSchemaEnumsMatchTheDomain(t *testing.T) {
	schema := loadSchema(t)

	tests := []struct {
		pointer string
		values  []string
	}{
		{
			pointer: "/$defs/action/properties/type",
			values: []string{
				string(wol.ActionTypeNoop), string(wol.ActionTypeSleep), string(wol.ActionTypeShutdown),
				string(wol.ActionTypeReboot), string(wol.ActionTypeExec), string(wol.ActionTypeHTTP),
				string(wol.ActionTypeSequence),
			},
		},
		{
			pointer: "/$defs/content/properties/kind",
			values: []string{
				string(wol.ContentAny), string(wol.ContentNone), string(wol.ContentSuffix),
				string(wol.ContentPrefix),
			},
		},
		{
			pointer: "/$defs/macBlock/properties/kind",
			values: []string{
				string(wol.MACAny), string(wol.MACSelf), string(wol.MACInterface), string(wol.MACExplicit),
			},
		},
		{pointer: "/$defs/command/properties/type", values: []string{string(wol.ActionTypeExec)}},
		{
			pointer: "/properties/server/properties/http/properties/auth/properties/type",
			// Empty means bearer (verified against the binary: an empty auth block complains
			// about the missing token, not about the type).
			values: []string{"", AuthTypeBearer, AuthTypeBasic, AuthTypeMTLS},
		},
		{
			pointer: "/properties/security/properties/remote_command_auth/properties/type",
			values:  []string{authTypeHMAC},
		},
		{
			pointer: "/properties/logging/properties/level",
			// The logging package keeps these as strings inside ParseLevel, so the list is
			// spelled out and cross-checked against the binary: "warning" and "" are accepted
			// aliases, "verbose" is rejected.
			values: []string{"", "debug", "info", "warn", "warning", "error"},
		},
		{
			pointer: "/properties/logging/properties/format",
			values:  []string{"", logging.FormatText, logging.FormatJSON},
		},
	}

	for _, tc := range tests {
		t.Run(tc.pointer, func(t *testing.T) {
			require.ElementsMatch(t, tc.values, enumValues(t, schema, tc.pointer))
		})
	}
}

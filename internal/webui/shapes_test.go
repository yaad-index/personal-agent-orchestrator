package webui_test

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

// jsonFields are the JSON names a Go type sends, embedded structs flattened.
func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for k := range jsonFields(f.Type) {
				out[k] = true
			}
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// tsFields are the fields a TypeScript interface in src declares.
func tsFields(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)export interface ` + name + `(?: extends \w+)? \{(.*?)\n\}`).FindStringSubmatch(src)
	require.NotNil(t, m, "interface %s in api.ts", name)
	var out []string
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		field, _, ok := strings.Cut(line, ":")
		require.True(t, ok, "%s: %q", name, line)
		out = append(out, strings.TrimSuffix(field, "?"))
	}
	return out
}

// Every field the UI reads of the node's JSON is one the node sends: a field
// the node does not send reads as undefined in the page and shows wrongly
// (the budget once showed "NaN%" this way).
func TestTheUIReadsOnlyFieldsTheNodeSends(t *testing.T) {
	b, err := os.ReadFile("../../ui/src/lib/api.ts")
	require.NoError(t, err)
	src := string(b)
	for ts, goType := range map[string]reflect.Type{
		"BudgetWindow":    reflect.TypeFor[local.BudgetWindowJSON](),
		"Budget":          reflect.TypeFor[local.BudgetJSON](),
		"NodeInfo":        reflect.TypeFor[local.NodeJSON](),
		"Agent":           reflect.TypeFor[local.NodeAgentJSON](),
		"Profile":         reflect.TypeFor[local.ProfileJSON](),
		"ProfileFile":     reflect.TypeFor[local.ProfileFileJSON](),
		"ProfileDraft":    reflect.TypeFor[local.ProfileDraftJSON](),
		"Recipe":          reflect.TypeFor[local.RecipeJSON](),
		"Spawned":         reflect.TypeFor[local.SpawnJSON](),
		"KillResult":      reflect.TypeFor[local.KillJSON](),
		"Task":            reflect.TypeFor[local.TaskJSON](),
		"TaskLogEntry":    reflect.TypeFor[task.LogEntry](),
		"TaskAttachments": reflect.TypeFor[task.Attachments](),
		"NewTask":         reflect.TypeFor[local.NewTaskJSON](),
		"Channel":         reflect.TypeFor[local.ChannelJSON](),
		"Binding":         reflect.TypeFor[local.StandingJSON](),
		"BindingList":     reflect.TypeFor[local.StandingListJSON](),
		"ChannelDefault":  reflect.TypeFor[local.DefaultJSON](),
		"ChannelDefaults": reflect.TypeFor[local.DefaultsJSON](),
		"DefaultChange":   reflect.TypeFor[local.DefaultChangeJSON](),
	} {
		sent := jsonFields(goType)
		for _, f := range tsFields(t, src, ts) {
			assert.True(t, sent[f], "api.ts %s.%s is not a field %s sends", ts, f, goType.Name())
		}
	}
}

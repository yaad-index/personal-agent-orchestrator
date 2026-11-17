package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

func TestAccessGrantPostsTheGrantAndSaysWhatItGave(t *testing.T) {
	changed := true
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.GrantChangeJSON{Changed: changed})
	})
	var out bytes.Buffer
	require.NoError(t, AccessGrantCmd{NodeFlags: s.flags(t), Subject: "agent:a", grantTarget: grantTarget{Tag: "team"}}.run(&out))
	assert.Equal(t, "POST /v1/access/grants", s.path)
	assert.JSONEq(t, `{"subject":"agent:a","kind":"agent","tag":"team"}`, s.body)
	assert.Equal(t, "agent:a may reach agents tagged team\n", out.String())

	changed = false
	out.Reset()
	require.NoError(t, AccessGrantCmd{NodeFlags: s.flags(t), Subject: "agent:a", grantTarget: grantTarget{Agent: "b"}}.run(&out))
	assert.Equal(t, "agent:a may reach agent b (already granted)\n", out.String())
}

func TestAccessRevokeDeletesByQuery(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.GrantChangeJSON{Changed: false})
	})
	var out bytes.Buffer
	require.NoError(t, AccessRevokeCmd{NodeFlags: s.flags(t), Subject: "agent:a", grantTarget: grantTarget{Agent: "b"}}.run(&out))
	assert.Equal(t, "DELETE /v1/access/grants?kind=agent&name=b&subject=agent%3Aa", s.path)
	assert.Equal(t, "agent:a no longer reaches agent b (it was not granted)\n", out.String())
}

func TestAccessGrantsListsOneRowEach(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.GrantsJSON{Version: 3, Grants: []local.GrantJSON{
			{Subject: "agent:a", Kind: "agent", Tag: "team"}, {Subject: "agent:a", Kind: "agent", Name: "b"}}})
	})
	var out bytes.Buffer
	require.NoError(t, AccessGrantsCmd{NodeFlags: s.flags(t), Subject: "agent:a"}.run(&out))
	assert.Equal(t, "GET /v1/access/grants?subject=agent%3Aa", s.path)
	assert.Equal(t, "agent:a\tagents tagged team\nagent:a\tagent b\n", out.String())
}

func TestAGrantNeedsExactlyOneTarget(t *testing.T) {
	f := NodeFlags{}
	assert.Error(t, AccessGrantCmd{NodeFlags: f, Subject: "agent:a"}.run(&bytes.Buffer{}))
	assert.Error(t, AccessRevokeCmd{NodeFlags: f, Subject: "agent:a", grantTarget: grantTarget{Agent: "b", Tag: "t"}}.run(&bytes.Buffer{}))
}

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// routeTemplateCorpus spells every one- and two-token template from the grammar's tricky
// tokens: parameters (named ":id" or empty), escaped colons, an inline verb, static text, a
// slash and the wildcard. No static token contains "id".
func routeTemplateCorpus() []string {
	tokens := []string{":id", ":", `\:`, `\:cancel`, "a", "/", "*"}
	templates := make([]string, 0, len(tokens)*(len(tokens)+1))
	for _, a := range tokens {
		templates = append(templates, "/"+a)
		for _, b := range tokens {
			templates = append(templates, "/"+a+b)
		}
	}
	return templates
}

type routePart struct {
	kind routePartKind
	text string
}

func TestWalkRouteTemplate(t *testing.T) {
	static := func(s string) routePart { return routePart{routePartStatic, s} }
	param := func(s string) routePart { return routePart{routePartParam, s} }
	wildcard := routePart{routePartWildcard, "*"}
	tests := []struct {
		name     string
		template string
		want     []routePart
	}{
		{name: "static", template: "/healthz", want: []routePart{static("/healthz")}},
		{name: "root", template: "/", want: []routePart{static("/")}},
		{name: "empty_is_root", template: "", want: []routePart{static("/")}},
		{name: "missing_slash", template: "cards/:pan", want: []routePart{static("/cards/"), param(":pan")}},
		{name: "mid_segment_param", template: "/files/f:name/raw", want: []routePart{static("/files/f"), param(":name"), static("/raw")}},
		{name: "empty_param_name", template: "/x/:", want: []routePart{static("/x/"), param(":")}},
		{name: "param_then_wildcard", template: "/files/:id/*", want: []routePart{static("/files/"), param(":id"), static("/"), wildcard}},
		{name: "wildcard_stops_walk", template: "/static/*rest/ignored/:x", want: []routePart{static("/static/"), wildcard}},
		{name: "bare_wildcard", template: "*", want: []routePart{static("/"), wildcard}},
		{name: "star_inside_param_name", template: "/odd/:a*b/tail", want: []routePart{static("/odd/"), param(":a*b"), static("/tail")}},
		{name: "escaped_colon_static", template: `/a\:b`, want: []routePart{static(`/a\:b`)}},
		{name: "inline_verb", template: `/orders/:id\:cancel`, want: []routePart{static("/orders/"), param(":id"), static(`\:cancel`)}},
		{name: "inline_verb_before_next_segment", template: `/x/:id\:c/:y`, want: []routePart{static("/x/"), param(":id"), static(`\:c/`), param(":y")}},
		{name: "inline_verb_holding_escaped_colon", template: `/x/:id\:a\:b`, want: []routePart{static("/x/"), param(":id"), static(`\:a\:b`)}},
		{name: "escaped_colon_before_param_colon", template: `/x/:a\:b:c`, want: []routePart{static("/x/"), param(`:a\:b:c`)}},
		{name: "colon_in_param_name_before_escaped_colon", template: `/x/:a:b\:c`, want: []routePart{static("/x/"), param(`:a:b\:c`)}},
		{name: "star_in_param_name_before_escaped_colon", template: `/x/:a*\:c`, want: []routePart{static("/x/"), param(`:a*\:c`)}},
		{name: "trailing_backslash_in_param_name", template: `/x/:a\`, want: []routePart{static("/x/"), param(`:a\`)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []routePart
			walkRouteTemplate(newRouteTemplate(tc.template), func(kind routePartKind, text string) {
				got = append(got, routePart{kind, text})
			})
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsParamStartAtIndexZero(t *testing.T) {
	assert.True(t, isParamStart(":x", 0))
	assert.False(t, isParamStart("/x", 0))
	assert.False(t, isParamStart(`\:x`, 1))
}

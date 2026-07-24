package provider_test

import (
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
)

func TestModelListPageRequiresBoundedCompleteOrCursor(t *testing.T) {
	valid := provider.ModelListPage{Complete: true, Models: []provider.Model{{ProviderModelID: "model-a", Lifecycle: provider.ModelUnknown}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, page := range map[string]provider.ModelListPage{
		"missing cursor":    {Models: valid.Models},
		"complete cursor":   {Complete: true, NextCursor: "next", Models: valid.Models},
		"unsorted":          {Complete: true, Models: []provider.Model{{ProviderModelID: "model-b", Lifecycle: provider.ModelUnknown}, {ProviderModelID: "model-a", Lifecycle: provider.ModelUnknown}}},
		"invalid lifecycle": {Complete: true, Models: []provider.Model{{ProviderModelID: "model-a", Lifecycle: provider.ModelLifecycle("future")}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := page.Validate(); err == nil {
				t.Fatal("invalid model-list page accepted")
			}
		})
	}
}

func TestModelListQueryBounds(t *testing.T) {
	for name, query := range map[string]provider.ModelListQuery{
		"missing endpoint": {Limit: 1},
		"zero limit":       {EndpointID: "endpoint", Limit: 0},
		"oversized limit":  {EndpointID: "endpoint", Limit: provider.ModelListMaxPageSize + 1},
		"unsafe cursor":    {EndpointID: "endpoint", Cursor: "bad\n", Limit: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := query.Validate(); err == nil {
				t.Fatal("invalid model-list query accepted")
			}
		})
	}
}

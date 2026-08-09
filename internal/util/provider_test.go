package util

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestGetProviderNameFallsBackToStaticCodexCatalog(t *testing.T) {
	const model = "gpt-5.6-sol"
	if providers := registry.GetGlobalRegistry().GetModelProviders(model); len(providers) != 0 {
		t.Fatalf("test requires no dynamic provider for %s, got %v", model, providers)
	}

	if got, want := GetProviderName(model), []string{"codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviderName(%q) = %v, want %v", model, got, want)
	}
}

func TestGetProviderNameKeepsUnknownModelUnroutable(t *testing.T) {
	if got := GetProviderName("definitely-unknown-cpa-model"); len(got) != 0 {
		t.Fatalf("unknown model providers = %v, want none", got)
	}
}

func TestGetProviderNamePrefersDynamicProviderForStaticCodexModel(t *testing.T) {
	const (
		clientID = "provider-static-codex-dynamic-test"
		model    = "gpt-5.6-sol"
		provider = "openai-compatible-test"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	if got, want := GetProviderName(model), []string{provider}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviderName(%q) = %v, want dynamic provider %v", model, got, want)
	}
}

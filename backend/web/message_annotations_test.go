package web

import (
	"context"
	"testing"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

type annotationTestPlugin struct{}

func (annotationTestPlugin) ID() string { return "annotation_test" }

func (annotationTestPlugin) Start(plugins.BackendStartHost) error { return nil }

func (annotationTestPlugin) Stop(plugins.BackendStartHost) error { return nil }

func (annotationTestPlugin) MessageAnnotations(_ context.Context, _ plugins.BackendHost, _ int64, _ []int64) (map[int64][]plugins.MessageAnnotation, error) {
	return map[int64][]plugins.MessageAnnotation{
		7: {{PluginID: "spoofed", Kind: "risk", Label: "High", Level: "high", Summary: "classified"}},
		8: {{Kind: "risk", Label: "not requested"}},
	}, nil
}

func TestPluginMessageAnnotationsRestrictsMessagesAndPluginIdentity(t *testing.T) {
	server := &Server{}
	got := server.pluginMessageAnnotations(context.Background(), 41, []int64{7}, []plugins.BackendPlugin{annotationTestPlugin{}})
	if len(got) != 1 || len(got[7]) != 1 {
		t.Fatalf("annotations = %#v", got)
	}
	if got[7][0].PluginID != "annotation_test" {
		t.Fatalf("plugin id = %q, want host-owned annotation_test", got[7][0].PluginID)
	}
	if _, ok := got[8]; ok {
		t.Fatalf("provider injected annotation for unrequested message: %#v", got[8])
	}
}

func TestPluginSettingChangeInvalidatesEveryTenantList(t *testing.T) {
	server := &Server{mailListCache: newMailListCache(), events: newEventHub()}
	firstID := int64(41)
	secondID := int64(42)
	server.rememberMailListPage(mailListCacheKey{UserID: firstID, Page: 1}, `"first"`, []byte(`{"page":1}`), 0)
	server.rememberMailListPage(mailListCacheKey{UserID: secondID, Page: 1}, `"second"`, []byte(`{"page":1}`), 0)
	changed, unsubscribe := server.events.Subscribe(firstID)
	defer unsubscribe()

	server.notifyPluginSettingChanged([]store.User{{ID: firstID}, {ID: secondID}})

	if got := server.mailListGeneration(firstID); got != 1 {
		t.Fatalf("first tenant generation = %d, want 1", got)
	}
	if got := server.mailListGeneration(secondID); got != 1 {
		t.Fatalf("second tenant generation = %d, want 1", got)
	}
	if _, ok := server.mailListCache.page(mailListCacheKey{UserID: firstID, Page: 1}); ok {
		t.Fatal("first tenant retained a plugin-decorated cached page")
	}
	if _, ok := server.mailListCache.page(mailListCacheKey{UserID: secondID, Page: 1}); ok {
		t.Fatal("second tenant retained a plugin-decorated cached page")
	}
	select {
	case <-changed:
	default:
		t.Fatal("active tenant was not notified of plugin setting change")
	}
}

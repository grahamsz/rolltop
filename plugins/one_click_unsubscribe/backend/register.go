package main

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/one_click_unsubscribe/history"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:               plugins.OneClickUnsubscribe,
		Name:             "One-click unsubscribe",
		Description:      "Detects RFC 8058 unsubscribe links and sends one-click unsubscribe requests.",
		EnabledByDefault: true,
	}, history.Migrations()...)
	plugins.RegisterHooks(plugins.OneClickUnsubscribe, oneClickUnsubscribeHook{})
}

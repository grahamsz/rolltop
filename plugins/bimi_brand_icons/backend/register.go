package main

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/bimi_brand_icons/bimi"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:               plugins.BIMIBrandIcons,
		Name:             "BIMI brand icons",
		Description:      "Fetches and caches verified BIMI SVG logos for sender domains.",
		EnabledByDefault: true,
	}, bimi.Migrations()...)
	plugins.RegisterHooks(plugins.BIMIBrandIcons, bimiBrandIconsHook{})
}

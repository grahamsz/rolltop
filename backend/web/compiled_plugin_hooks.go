// File overview: Web-layer dispatch helpers for compiled plugin hooks.

package web

import "rolltop/backend/plugins"

func oneClickUnsubscribeHook() (plugins.OneClickUnsubscribeHook, bool) {
	for _, hook := range plugins.Hooks(plugins.OneClickUnsubscribe) {
		typed, ok := hook.(plugins.OneClickUnsubscribeHook)
		if ok {
			return typed, true
		}
	}
	return nil, false
}

func trustedImageSourcesHook() (plugins.TrustedImageSourcesHook, bool) {
	for _, hook := range plugins.Hooks(plugins.TrustedImageSources) {
		typed, ok := hook.(plugins.TrustedImageSourcesHook)
		if ok {
			return typed, true
		}
	}
	return nil, false
}

func remoteImageBlocklistHook() (plugins.RemoteImageBlocklistHook, bool) {
	for _, hook := range plugins.Hooks(plugins.RemoteImageBlocklist) {
		typed, ok := hook.(plugins.RemoteImageBlocklistHook)
		if ok {
			return typed, true
		}
	}
	return nil, false
}

func bimiBrandIconsHook() (plugins.BIMIHook, bool) {
	for _, hook := range plugins.Hooks(plugins.BIMIBrandIcons) {
		typed, ok := hook.(plugins.BIMIHook)
		if ok {
			return typed, true
		}
	}
	return nil, false
}

func gravatarSenderIconsHook() (plugins.GravatarHook, bool) {
	for _, hook := range plugins.Hooks(plugins.GravatarSenderIcons) {
		typed, ok := hook.(plugins.GravatarHook)
		if ok {
			return typed, true
		}
	}
	return nil, false
}

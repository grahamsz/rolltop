package main

import (
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func storeFromHost(host plugins.BackendHost) *store.Store {
	db, _ := host.Store().(*store.Store)
	return db
}

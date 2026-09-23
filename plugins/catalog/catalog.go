package catalog

import (
	"rolltop/backend/plugins"
	carddavschema "rolltop/plugins/carddav_sync/schema"
	pgpschema "rolltop/plugins/client_side_pgp/schema"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:           plugins.ClientSidePGP,
		Name:         "Client-side PGP",
		Description:  "Adds browser-loaded OpenPGP decrypt, verify, sign, encrypt, Autocrypt, and key-management UI.",
		Heavy:        true,
		Experimental: true,
	}, pgpschema.Migrations()...)
	plugins.Register(plugins.Definition{
		ID:           plugins.CardDAVSync,
		Name:         "CardDAV sync",
		Description:  "Syncs contacts one way from a CardDAV address book into Rolltop with incremental updates.",
		Experimental: true,
	}, carddavschema.Migrations()...)
}

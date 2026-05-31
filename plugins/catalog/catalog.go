package catalog

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/client_side_pgp/schema"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:           plugins.ClientSidePGP,
		Name:         "Client-side PGP",
		Description:  "Adds browser-loaded OpenPGP decrypt, verify, sign, encrypt, Autocrypt, and key-management UI.",
		Heavy:        true,
		Experimental: true,
	}, schema.Migrations()...)
}

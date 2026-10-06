package catalog

import (
	"rolltop/backend/plugins"
	invitesschema "rolltop/plugins/calendar_invites/schema"
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
		ID:           plugins.CalendarInvites,
		Name:         "Calendar invites",
		Description:  "Detects calendar invitations in incoming mail, sends RSVP replies, and optionally pushes events to a CalDAV calendar.",
		Experimental: true,
	}, invitesschema.Migrations()...)
	plugins.Register(plugins.Definition{
		ID:           plugins.CardDAVSync,
		Name:         "CardDAV sync",
		Description:  "Imports contacts one way from a CardDAV address book into Rolltop with incremental updates.",
		Experimental: true,
	}, carddavschema.Migrations()...)
}

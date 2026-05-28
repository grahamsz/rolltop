// Package bundled imports every Go plugin compiled with the main server.
package bundled

import (
	_ "rolltop/plugins/attachment_preview"
	_ "rolltop/plugins/bimi_brand_icons"
	_ "rolltop/plugins/client_side_pgp"
	_ "rolltop/plugins/gravatar_sender_icons"
	_ "rolltop/plugins/language_search"
	_ "rolltop/plugins/one_click_unsubscribe"
	_ "rolltop/plugins/remote_image_blocklist"
	_ "rolltop/plugins/trusted_image_sources"
)

package model

import _ "embed"

//go:embed model.bin
var embeddedModel []byte

//go:embed model.json
var embeddedMetadata []byte

// LoadEmbedded loads the checked-in, verified model without filesystem or
// network access.
func LoadEmbedded() (*Classifier, error) {
	return loadWithMetadata(embeddedModel, embeddedMetadata)
}

package main

import (
	"context"
	"database/sql"

	"rolltop/backend/plugins"
	"rolltop/plugins/bimi_brand_icons/bimi"
)

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return plugins.NoopBackendPlugin{PluginID: plugins.BIMIBrandIcons}
}

type bimiBrandIconsHook struct{}

func (bimiBrandIconsHook) NormalizeDomain(value string) string {
	return bimi.NormalizeDomain(value)
}

func (bimiBrandIconsHook) AssetURL(domain string) string {
	return bimi.AssetURL(domain)
}

func (bimiBrandIconsHook) GetIcon(ctx context.Context, db *sql.DB, userID int64, domain string) (plugins.BIMIIcon, error) {
	icon, err := bimi.GetIcon(ctx, db, userID, domain)
	if err != nil {
		return plugins.BIMIIcon{}, err
	}
	return plugins.BIMIIcon{
		ID:        icon.ID,
		UserID:    icon.UserID,
		Domain:    icon.Domain,
		LogoURL:   icon.LogoURL,
		SVG:       icon.SVG,
		Status:    icon.Status,
		Error:     icon.Error,
		FetchedAt: icon.FetchedAt,
		ExpiresAt: icon.ExpiresAt,
		UpdatedAt: icon.UpdatedAt,
	}, nil
}

func (bimiBrandIconsHook) UpsertIcon(ctx context.Context, db *sql.DB, icon plugins.BIMIIcon) error {
	return bimi.UpsertIcon(ctx, db, bimi.Icon{
		ID:        icon.ID,
		UserID:    icon.UserID,
		Domain:    icon.Domain,
		LogoURL:   icon.LogoURL,
		SVG:       icon.SVG,
		Status:    icon.Status,
		Error:     icon.Error,
		FetchedAt: icon.FetchedAt,
		ExpiresAt: icon.ExpiresAt,
		UpdatedAt: icon.UpdatedAt,
	})
}

func (bimiBrandIconsHook) Fetch(ctx context.Context, domain string) (plugins.BIMIResult, error) {
	result := bimi.Resolver{}.Fetch(ctx, domain)
	return plugins.BIMIResult{
		Domain:    result.Domain,
		LogoURL:   result.LogoURL,
		SVG:       result.SVG,
		Status:    result.Status,
		Error:     result.Error,
		FetchedAt: result.FetchedAt,
		ExpiresAt: result.ExpiresAt,
	}, nil
}

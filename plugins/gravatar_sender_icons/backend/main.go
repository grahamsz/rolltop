package main

import (
	"context"
	"database/sql"
	"time"

	"rolltop/backend/plugins"
	"rolltop/plugins/gravatar_sender_icons/gravatar"
)

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return plugins.NoopBackendPlugin{PluginID: plugins.GravatarSenderIcons}
}

type gravatarSenderIconsHook struct{}

func (gravatarSenderIconsHook) NormalizeHash(value string) string {
	return gravatar.NormalizeHash(value)
}
func (gravatarSenderIconsHook) AssetURL(hash string) string         { return gravatar.AssetURL(hash) }
func (gravatarSenderIconsHook) Hash(email string) string            { return gravatar.Hash(email) }
func (gravatarSenderIconsHook) FetchURL(hash string) string         { return gravatar.FetchURL(hash) }
func (gravatarSenderIconsHook) ErrorTTL(now time.Time) time.Time    { return gravatar.ErrorTTL(now) }
func (gravatarSenderIconsHook) MissingTTL(now time.Time) time.Time  { return gravatar.MissingTTL(now) }
func (gravatarSenderIconsHook) PositiveTTL(now time.Time) time.Time { return gravatar.PositiveTTL(now) }
func (gravatarSenderIconsHook) MaxImageBytes() int64                { return gravatar.MaxImageBytes }

func (gravatarSenderIconsHook) GetImage(ctx context.Context, db *sql.DB, userID int64, emailHash string) (plugins.GravatarImage, error) {
	image, err := gravatar.GetImage(ctx, db, userID, emailHash)
	if err != nil {
		return plugins.GravatarImage{}, err
	}
	return plugins.GravatarImage{
		ID:          image.ID,
		UserID:      image.UserID,
		EmailHash:   image.EmailHash,
		ContentType: image.ContentType,
		Image:       image.Image,
		Status:      image.Status,
		Error:       image.Error,
		FetchedAt:   image.FetchedAt,
		ExpiresAt:   image.ExpiresAt,
		UpdatedAt:   image.UpdatedAt,
	}, nil
}

func (gravatarSenderIconsHook) UpsertImage(ctx context.Context, db *sql.DB, image plugins.GravatarImage) error {
	return gravatar.UpsertImage(ctx, db, gravatar.Image{
		ID:          image.ID,
		UserID:      image.UserID,
		EmailHash:   image.EmailHash,
		ContentType: image.ContentType,
		Image:       image.Image,
		Status:      image.Status,
		Error:       image.Error,
		FetchedAt:   image.FetchedAt,
		ExpiresAt:   image.ExpiresAt,
		UpdatedAt:   image.UpdatedAt,
	})
}

// File overview: Contact merge rules for CardDAV sync. Updates follow the
// core vCard import behavior: fill empty scalar fields and union child rows
// (emails, phones, addresses, URLs) without ever deleting user data.

package main

import (
	"strings"

	"rolltop/backend/store"
)

// mergeSyncedContact merges an incoming vCard contact into an existing
// Rolltop contact the way the core vCard import does.
func mergeSyncedContact(existing, incoming store.Contact) store.Contact {
	merged := existing
	if strings.TrimSpace(merged.NamePrefix) == "" {
		merged.NamePrefix = incoming.NamePrefix
	}
	if strings.TrimSpace(merged.GivenName) == "" {
		merged.GivenName = incoming.GivenName
	}
	if strings.TrimSpace(merged.AdditionalName) == "" {
		merged.AdditionalName = incoming.AdditionalName
	}
	if strings.TrimSpace(merged.FamilyName) == "" {
		merged.FamilyName = incoming.FamilyName
	}
	if strings.TrimSpace(merged.NameSuffix) == "" {
		merged.NameSuffix = incoming.NameSuffix
	}
	if strings.TrimSpace(merged.DisplayName) == "" {
		merged.DisplayName = incoming.DisplayName
	}
	if strings.TrimSpace(merged.Nickname) == "" {
		merged.Nickname = incoming.Nickname
	}
	if strings.TrimSpace(merged.Organization) == "" {
		merged.Organization = incoming.Organization
	}
	if strings.TrimSpace(merged.Department) == "" {
		merged.Department = incoming.Department
	}
	if strings.TrimSpace(merged.JobTitle) == "" {
		merged.JobTitle = incoming.JobTitle
	}
	if strings.TrimSpace(merged.Birthday) == "" {
		merged.Birthday = incoming.Birthday
	}
	if strings.TrimSpace(merged.Notes) == "" {
		merged.Notes = incoming.Notes
	}
	if strings.TrimSpace(merged.Categories) == "" {
		merged.Categories = incoming.Categories
	}
	merged.Emails = mergeSyncedEmails(merged.Emails, incoming.Emails)
	merged.Phones = mergeSyncedPhones(merged.Phones, incoming.Phones)
	merged.Addresses = mergeSyncedAddresses(merged.Addresses, incoming.Addresses)
	merged.URLs = mergeSyncedURLs(merged.URLs, incoming.URLs)
	return merged
}

func mergeSyncedEmails(existing, incoming []store.ContactEmail) []store.ContactEmail {
	seen := map[string]bool{}
	out := append([]store.ContactEmail{}, existing...)
	for _, email := range existing {
		seen[store.NormalizeContactEmail(email.Email)] = true
	}
	for _, email := range incoming {
		key := store.NormalizeContactEmail(email.Email)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, email)
	}
	return out
}

func mergeSyncedPhones(existing, incoming []store.ContactPhone) []store.ContactPhone {
	seen := map[string]bool{}
	out := append([]store.ContactPhone{}, existing...)
	for _, phone := range existing {
		seen[strings.ToLower(strings.TrimSpace(phone.Number))] = true
	}
	for _, phone := range incoming {
		key := strings.ToLower(strings.TrimSpace(phone.Number))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, phone)
	}
	return out
}

func mergeSyncedAddresses(existing, incoming []store.ContactAddress) []store.ContactAddress {
	seen := map[string]bool{}
	out := append([]store.ContactAddress{}, existing...)
	for _, addr := range existing {
		seen[syncedAddressKey(addr)] = true
	}
	for _, addr := range incoming {
		key := syncedAddressKey(addr)
		if key == "||||" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, addr)
	}
	return out
}

func mergeSyncedURLs(existing, incoming []store.ContactURL) []store.ContactURL {
	seen := map[string]bool{}
	out := append([]store.ContactURL{}, existing...)
	for _, u := range existing {
		seen[strings.ToLower(strings.TrimSpace(u.URL))] = true
	}
	for _, u := range incoming {
		key := strings.ToLower(strings.TrimSpace(u.URL))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, u)
	}
	return out
}

func syncedAddressKey(addr store.ContactAddress) string {
	return strings.ToLower(strings.Join([]string{
		strings.TrimSpace(addr.Street),
		strings.TrimSpace(addr.Locality),
		strings.TrimSpace(addr.Region),
		strings.TrimSpace(addr.PostalCode),
		strings.TrimSpace(addr.Country),
	}, "|"))
}

// contactIsEmpty reports whether a parsed vCard carries anything worth
// storing, mirroring the core import's skip rule.
func contactIsEmpty(c store.Contact) bool {
	return len(c.Emails) == 0 &&
		strings.TrimSpace(c.DisplayName+c.GivenName+c.FamilyName+c.Organization) == ""
}

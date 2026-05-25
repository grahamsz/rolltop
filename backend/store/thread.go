package store

import (
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var messageIDRE = regexp.MustCompile(`<([^<>]+)>`)

func ThreadKey(messageID, inReplyTo, referencesHeader, subject string) string {
	if ids := messageIDs(referencesHeader); len(ids) > 0 {
		return "msgid:" + ids[0]
	}
	if ids := messageIDs(inReplyTo); len(ids) > 0 {
		return "msgid:" + ids[0]
	}
	if ids := messageIDs(messageID); len(ids) > 0 {
		return "msgid:" + ids[0]
	}
	if normalized := normalizedThreadSubject(subject); normalized != "" {
		return "subject:" + normalized
	}
	return ""
}

func NormalizedThreadSubject(subject string) string {
	return normalizedThreadSubject(subject)
}

func messageIDs(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	var out []string
	for _, m := range messageIDRE.FindAllStringSubmatch(value, -1) {
		id := strings.TrimSpace(m[1])
		if id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 && strings.Contains(value, "@") {
		out = append(out, strings.Trim(value, "<> \t\r\n"))
	}
	return out
}

func normalizedThreadSubject(subject string) string {
	subject = strings.ToLower(strings.TrimSpace(subject))
	for {
		next := strings.TrimSpace(subject)
		for _, prefix := range []string{"re:", "fw:", "fwd:"} {
			if strings.HasPrefix(next, prefix) {
				next = strings.TrimSpace(strings.TrimPrefix(next, prefix))
			}
		}
		if next == subject {
			break
		}
		subject = next
	}
	var b strings.Builder
	var lastSpace bool
	for _, r := range subject {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func SenderIdentity(from string) string {
	from = strings.TrimSpace(from)
	if from == "" {
		return ""
	}
	if addrs, err := mail.ParseAddressList(from); err == nil && len(addrs) > 0 {
		return strings.ToLower(strings.TrimSpace(addrs[0].Address))
	}
	return strings.ToLower(strings.Trim(from, "<> \t\r\n"))
}

func sortSenderStats(stats []SenderReadStat) {
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Boost == stats[j].Boost {
			if stats[i].ReadCount == stats[j].ReadCount {
				return stats[i].Sender < stats[j].Sender
			}
			return stats[i].ReadCount > stats[j].ReadCount
		}
		return stats[i].Boost > stats[j].Boost
	})
}

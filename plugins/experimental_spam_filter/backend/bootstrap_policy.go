package main

import (
	"net/mail"
	"sort"
	"strings"
	"time"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	bootstrapMaximumPerClass       = 500
	bootstrapMaximumHamExamined    = 2000
	bootstrapMaximumPerSender      = 20
	bootstrapMinimumSenderReads    = 3
	bootstrapMinimumSenderReadRate = 0.80
	bootstrapMinimumSenderSpan     = 14 * 24 * time.Hour
	bootstrapMinimumAge            = 48 * time.Hour
	bootstrapMaximumHamProbability = 0.45
	bootstrapStrongSpamImpact      = 2.0
)

type bootstrapEnvelope struct {
	AccountID    int64
	MailboxID    int64
	MailboxName  string
	UID          uint32
	MessageID    string
	From         string
	To           string
	CC           string
	Subject      string
	Date         time.Time
	InternalDate time.Time
	Size         int64
	Flags        []string
}

type bootstrapSenderStat struct {
	Total     int
	Read      int
	FirstRead time.Time
	LastRead  time.Time
}

func bootstrapSenderAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if address, err := mail.ParseAddress(value); err == nil {
		return strings.ToLower(strings.TrimSpace(address.Address))
	}
	return ""
}

func bootstrapSeen(flags []string) bool {
	for _, flag := range flags {
		if strings.EqualFold(strings.TrimSpace(flag), `\Seen`) {
			return true
		}
	}
	return false
}

func bootstrapEstablishedSenders(messages []bootstrapEnvelope) map[string]bootstrapSenderStat {
	stats := make(map[string]bootstrapSenderStat)
	for _, message := range messages {
		sender := bootstrapSenderAddress(message.From)
		if sender == "" {
			continue
		}
		stat := stats[sender]
		stat.Total++
		if bootstrapSeen(message.Flags) {
			stat.Read++
			date := message.InternalDate
			if date.IsZero() {
				date = message.Date
			}
			if stat.FirstRead.IsZero() || date.Before(stat.FirstRead) {
				stat.FirstRead = date
			}
			if stat.LastRead.IsZero() || date.After(stat.LastRead) {
				stat.LastRead = date
			}
		}
		stats[sender] = stat
	}
	for sender, stat := range stats {
		readRate := 0.0
		if stat.Total > 0 {
			readRate = float64(stat.Read) / float64(stat.Total)
		}
		if stat.Read < bootstrapMinimumSenderReads || readRate < bootstrapMinimumSenderReadRate ||
			stat.FirstRead.IsZero() || stat.LastRead.Sub(stat.FirstRead) < bootstrapMinimumSenderSpan {
			delete(stats, sender)
		}
	}
	return stats
}

func bootstrapHamMetadataCandidates(messages []bootstrapEnvelope, now time.Time) []bootstrapEnvelope {
	established := bootstrapEstablishedSenders(messages)
	selected := make([]bootstrapEnvelope, 0, min(bootstrapMaximumHamExamined, len(messages)))
	perSender := make(map[string]int)
	sort.SliceStable(messages, func(i, j int) bool {
		left, right := messages[i].InternalDate, messages[j].InternalDate
		if left.IsZero() {
			left = messages[i].Date
		}
		if right.IsZero() {
			right = messages[j].Date
		}
		return left.After(right)
	})
	for _, message := range messages {
		if len(selected) >= bootstrapMaximumHamExamined || !bootstrapSeen(message.Flags) {
			continue
		}
		date := message.InternalDate
		if date.IsZero() {
			date = message.Date
		}
		if date.IsZero() || date.After(now.Add(-bootstrapMinimumAge)) {
			continue
		}
		sender := bootstrapSenderAddress(message.From)
		if _, ok := established[sender]; !ok || perSender[sender] >= bootstrapMaximumPerSender {
			continue
		}
		perSender[sender]++
		selected = append(selected, message)
	}
	return selected
}

func bootstrapAutomaticHamEligible(classifier *spammodel.Classifier, message spammodel.Message) bool {
	if classifier == nil {
		return false
	}
	score, err := classifier.Classify(message)
	if err != nil || score.Probability >= bootstrapMaximumHamProbability {
		return false
	}
	for _, contribution := range score.Contributions {
		if contribution.Impact >= bootstrapStrongSpamImpact {
			return false
		}
	}
	return true
}

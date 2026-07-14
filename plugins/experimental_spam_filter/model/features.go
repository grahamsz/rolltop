package model

import (
	"net"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// FeatureSchema changes whenever rule IDs or semantics change. A checked-in
	// score artifact may only be loaded with the exact schema that produced it.
	FeatureSchema = "rolltop-named-spam-rules-v1"
	MaxBodyBytes  = 64 << 10
	maxURLs       = 32
)

// Message is the bounded, decoded view available both during corpus mass-checks
// and at runtime. The model package never stores message content.
type Message struct {
	Subject         string   `json:"subject"`
	Body            string   `json:"body"`
	From            string   `json:"from"`
	To              []string `json:"to,omitempty"`
	MIMEType        string   `json:"mime_type,omitempty"`
	AttachmentTypes []string `json:"attachment_types,omitempty"`
	HTML            bool     `json:"html,omitempty"`
}

// Feature is a hit for one authored rule. Index and Name have a one-to-one,
// versioned mapping; there is no hashing and no data-derived feature name.
type Feature struct {
	Index uint32
	Name  string
	Value float64
}

type RulePolarity int8

const (
	HamRule  RulePolarity = -1
	SpamRule RulePolarity = 1
)

// RuleDefinition is the public, auditable part of the runtime rule table.
// MaxScore bounds the absolute fitted score. Count rules are capped by their
// evaluator, so a repeated phrase cannot grow without limit.
type RuleDefinition struct {
	Index       uint32       `json:"index"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Polarity    RulePolarity `json:"polarity"`
	MaxScore    float64      `json:"max_score"`
	Count       bool         `json:"count"`
}

type authoredRule struct {
	RuleDefinition
	evaluate func(*ruleContext) float64
}

type ruleContext struct {
	message       Message
	subject       string
	body          string
	subjectTokens []string
	bodyTokens    []string
	fromEmpty     bool
	fromValid     bool
	urls          []*url.URL
}

func boolean(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func capCount(value, maximum int) float64 {
	if value > maximum {
		value = maximum
	}
	return float64(value)
}

func phrases(values ...string) [][]string {
	result := make([][]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.Fields(value))
	}
	return result
}

func phraseHits(tokens []string, candidates [][]string) int {
	hits := 0
	for _, candidate := range candidates {
		if len(candidate) == 0 || len(candidate) > len(tokens) {
			continue
		}
		for start := 0; start+len(candidate) <= len(tokens); start++ {
			match := true
			for offset := range candidate {
				if tokens[start+offset] != candidate[offset] {
					match = false
					break
				}
			}
			if match {
				hits++
			}
		}
	}
	return hits
}

func anyPhrase(tokens []string, candidates ...string) bool {
	return phraseHits(tokens, phrases(candidates...)) > 0
}

func phraseCount(tokens []string, maximum int, candidates ...string) float64 {
	return capCount(phraseHits(tokens, phrases(candidates...)), maximum)
}

func combinedPhraseCount(context *ruleContext, maximum int, candidates ...string) float64 {
	count := phraseHits(context.subjectTokens, phrases(candidates...)) + phraseHits(context.bodyTokens, phrases(candidates...))
	return capCount(count, maximum)
}

var authoredRules = []authoredRule{
	{RuleDefinition: RuleDefinition{Name: "HEADER_MISSING_SUBJECT", Description: "Subject header is empty", Polarity: SpamRule, MaxScore: 0.8}, evaluate: func(c *ruleContext) float64 { return boolean(strings.TrimSpace(c.subject) == "") }},
	{RuleDefinition: RuleDefinition{Name: "HEADER_MISSING_FROM", Description: "From header is empty", Polarity: SpamRule, MaxScore: 1.5}, evaluate: func(c *ruleContext) float64 { return boolean(c.fromEmpty) }},
	{RuleDefinition: RuleDefinition{Name: "HEADER_UNPARSEABLE_FROM", Description: "From header is not a valid mailbox", Polarity: SpamRule, MaxScore: 1.2}, evaluate: func(c *ruleContext) float64 { return boolean(!c.fromEmpty && !c.fromValid) }},
	{RuleDefinition: RuleDefinition{Name: "HEADER_MANY_RECIPIENTS", Description: "Ten or more visible recipients", Polarity: SpamRule, MaxScore: 1.2}, evaluate: func(c *ruleContext) float64 { return boolean(messageRecipientCount(c) >= 10) }},
	{RuleDefinition: RuleDefinition{Name: "HEADER_NO_VISIBLE_RECIPIENT", Description: "No visible To or Cc recipient", Polarity: SpamRule, MaxScore: 0.8}, evaluate: func(c *ruleContext) float64 { return boolean(messageRecipientCount(c) == 0) }},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_ALL_CAPS", Description: "Subject letters are predominantly uppercase", Polarity: SpamRule, MaxScore: 1.3}, evaluate: func(c *ruleContext) float64 {
		return boolean(uppercaseRatio(c.subject) >= .85 && letterCount(c.subject) >= 8)
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_REPEATED_PUNCTUATION", Description: "Subject contains repeated exclamation or question marks", Polarity: SpamRule, MaxScore: 1.0}, evaluate: func(c *ruleContext) float64 {
		return boolean(strings.Contains(c.subject, "!!") || strings.Contains(c.subject, "??"))
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_CURRENCY_AMOUNT", Description: "Subject advertises a numeric currency amount", Polarity: SpamRule, MaxScore: 1.5}, evaluate: func(c *ruleContext) float64 { return boolean(hasCurrencyAmount(c.subject)) }},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_FREE_OFFER", Description: "Subject contains an explicit free-offer phrase", Polarity: SpamRule, MaxScore: 2.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "free offer", "free gift", "free trial", "absolutely free", "free cash", "free money")
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_PRIZE_WINNER", Description: "Subject announces a prize or winner", Polarity: SpamRule, MaxScore: 3.2, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "you are a winner", "you have won", "claim your prize", "cash prize", "prize winner", "winning notification")
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_URGENT_ACTION", Description: "Subject demands urgent action", Polarity: SpamRule, MaxScore: 2.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "act now", "urgent response", "urgent action", "respond immediately", "limited time", "last chance")
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_MONEY_PROMISE", Description: "Subject promises cash or income", Polarity: SpamRule, MaxScore: 2.5, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "make money", "extra income", "earn cash", "cash bonus", "million dollars", "financial freedom")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_PRIZE_CLAIM", Description: "Body contains prize-claim language", Polarity: SpamRule, MaxScore: 3.5, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "claim your prize", "claim your reward", "you have won", "selected winner", "winning notification", "cash prize", "prize money")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_FREE_OFFER", Description: "Body contains explicit free-offer language", Polarity: SpamRule, MaxScore: 2.4, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "free offer", "free gift", "free trial", "absolutely free", "free cash", "free money", "no cost to you")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_CALL_TO_ACTION", Description: "Body uses direct purchase or click commands", Polarity: SpamRule, MaxScore: 2.2, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "click here", "click below", "act now", "order now", "buy now", "apply now", "sign up now")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_URGENT_DEADLINE", Description: "Body applies an urgent deadline", Polarity: SpamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "limited time offer", "offer expires", "respond immediately", "urgent response", "last chance", "do not delay", "expires today")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_GUARANTEE_PROMISE", Description: "Body makes an explicit guarantee", Polarity: SpamRule, MaxScore: 2.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "money back guarantee", "satisfaction guaranteed", "guaranteed approval", "guaranteed income", "guaranteed results", "no obligation")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_EASY_MONEY", Description: "Body promises easy or fast income", Polarity: SpamRule, MaxScore: 3.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "make money fast", "make money from home", "earn extra income", "earn cash", "financial freedom", "easy money", "get rich")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_LOTTERY_SWEEPSTAKES", Description: "Body refers to lottery or sweepstakes winnings", Polarity: SpamRule, MaxScore: 3.5, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "lottery winner", "lottery winnings", "winning numbers", "sweepstakes winner", "grand prize", "prize draw")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_DEBT_CREDIT", Description: "Body markets debt or credit relief", Polarity: SpamRule, MaxScore: 2.6, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "debt relief", "eliminate debt", "credit repair", "bad credit", "credit score", "consolidate your debt", "unsecured credit")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_MORTGAGE_LOAN", Description: "Body markets mortgages or instant loans", Polarity: SpamRule, MaxScore: 2.4, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "mortgage rates", "refinance your home", "home loan", "instant loan", "loan approval", "payday loan", "low interest rate")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_PHARMACEUTICAL", Description: "Body markets prescription drugs without a prescription", Polarity: SpamRule, MaxScore: 3.6, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "no prescription", "online pharmacy", "prescription drugs", "generic viagra", "generic cialis", "cheap pills", "order medication")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_WEIGHT_LOSS", Description: "Body promises rapid weight loss", Polarity: SpamRule, MaxScore: 2.5, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "lose weight fast", "weight loss", "burn fat", "diet pill", "shed pounds")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_ADULT_OFFER", Description: "Body contains explicit adult-service advertising", Polarity: SpamRule, MaxScore: 3.2, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "adult entertainment", "adult dating", "meet singles", "live webcam", "xxx pictures")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_INVESTMENT_CRYPTO", Description: "Body promises exceptional investment or crypto returns", Polarity: SpamRule, MaxScore: 2.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "investment opportunity", "guaranteed returns", "double your money", "crypto profits", "bitcoin profits", "high yield investment")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_WIRE_TRANSFER", Description: "Body requests a wire transfer or bank details", Polarity: SpamRule, MaxScore: 2.7, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "wire transfer", "bank account details", "beneficiary account", "transfer the funds", "western union")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_WORK_FROM_HOME", Description: "Body advertises work-at-home income", Polarity: SpamRule, MaxScore: 2.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "work from home", "home based business", "be your own boss", "no experience required", "part time income")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_MARKETING_SUPERLATIVES", Description: "Body repeats authored marketing superlatives", Polarity: SpamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "once in a lifetime", "amazing opportunity", "incredible offer", "lowest price", "best price", "special promotion")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_RISK_FREE", Description: "Body claims an offer has no risk", Polarity: SpamRule, MaxScore: 2.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "risk free", "no risk", "nothing to lose", "no strings attached")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_REMOVE_INSTRUCTIONS", Description: "Body contains legacy bulk-mail removal instructions", Polarity: SpamRule, MaxScore: 1.2, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 2, "remove in the subject", "type remove", "reply remove", "send remove")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_MULTIPLE_CURRENCY_AMOUNTS", Description: "Body contains several numeric currency amounts", Polarity: SpamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 { return capCount(currencyAmountCount(c.body), 3) }},
	{RuleDefinition: RuleDefinition{Name: "BODY_PHONE_ORDER", Description: "Body asks the recipient to call or phone an order", Polarity: SpamRule, MaxScore: 1.4, Count: true}, evaluate: func(c *ruleContext) float64 {
		return combinedPhraseCount(c, 2, "call now", "call today", "phone order", "order by phone", "toll free")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_HIGH_UPPERCASE_RATIO", Description: "A sufficiently long body is predominantly uppercase", Polarity: SpamRule, MaxScore: 1.2}, evaluate: func(c *ruleContext) float64 {
		return boolean(letterCount(c.body) >= 40 && uppercaseRatio(c.body) >= .72)
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_MANY_EXCLAMATIONS", Description: "Body contains at least eight exclamation marks", Polarity: SpamRule, MaxScore: 1.0, Count: true}, evaluate: func(c *ruleContext) float64 { return capCount(strings.Count(c.body, "!")/8, 3) }},
	{RuleDefinition: RuleDefinition{Name: "URI_MANY_LINKS", Description: "Message contains at least four web links", Polarity: SpamRule, MaxScore: 1.7, Count: true}, evaluate: func(c *ruleContext) float64 {
		if len(c.urls) < 4 {
			return 0
		}
		return capCount(len(c.urls)-3, 4)
	}},
	{RuleDefinition: RuleDefinition{Name: "URI_NUMERIC_HOST", Description: "A web link uses a literal IP address", Polarity: SpamRule, MaxScore: 2.4, Count: true}, evaluate: func(c *ruleContext) float64 {
		count := 0
		for _, u := range c.urls {
			if net.ParseIP(u.Hostname()) != nil {
				count++
			}
		}
		return capCount(count, 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "URI_USERINFO", Description: "A web link contains username information", Polarity: SpamRule, MaxScore: 2.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		count := 0
		for _, u := range c.urls {
			if u.User != nil {
				count++
			}
		}
		return capCount(count, 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "URI_NONSTANDARD_PORT", Description: "A web link uses a nonstandard explicit port", Polarity: SpamRule, MaxScore: 1.2, Count: true}, evaluate: func(c *ruleContext) float64 {
		count := 0
		for _, u := range c.urls {
			if port := u.Port(); port != "" && port != "80" && port != "443" {
				count++
			}
		}
		return capCount(count, 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "URI_PERCENT_ENCODED", Description: "A web link contains extensive percent encoding", Polarity: SpamRule, MaxScore: 1.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		count := 0
		for _, u := range c.urls {
			if strings.Count(u.EscapedPath()+u.RawQuery, "%") >= 3 {
				count++
			}
		}
		return capCount(count, 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "URI_LONG_QUERY", Description: "A web link has an unusually long query string", Polarity: SpamRule, MaxScore: 1.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		count := 0
		for _, u := range c.urls {
			if len(u.RawQuery) >= 120 {
				count++
			}
		}
		return capCount(count, 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_HTML_MESSAGE", Description: "Message contains an HTML body", Polarity: SpamRule, MaxScore: 0.8}, evaluate: func(c *ruleContext) float64 {
		return boolean(c.message.HTML || strings.Contains(normalizeMIME(c.message.MIMEType), "html"))
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_HTML_WITHOUT_TEXT", Description: "HTML message has almost no decoded text", Polarity: SpamRule, MaxScore: 1.6}, evaluate: func(c *ruleContext) float64 { return boolean(c.message.HTML && len(c.bodyTokens) < 8) }},
	{RuleDefinition: RuleDefinition{Name: "MIME_EXECUTABLE_ATTACHMENT", Description: "Message declares an executable or script attachment", Polarity: SpamRule, MaxScore: 3.2, Count: true}, evaluate: func(c *ruleContext) float64 {
		return capCount(attachmentMatches(c.message.AttachmentTypes, executableMIMEs), 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_ARCHIVE_ATTACHMENT", Description: "Message declares a compressed archive attachment", Polarity: SpamRule, MaxScore: 1.6, Count: true}, evaluate: func(c *ruleContext) float64 {
		return capCount(attachmentMatches(c.message.AttachmentTypes, archiveMIMEs), 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_MANY_ATTACHMENTS", Description: "Message declares at least four attachments", Polarity: SpamRule, MaxScore: 1.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		if len(c.message.AttachmentTypes) < 4 {
			return 0
		}
		return capCount(len(c.message.AttachmentTypes)-3, 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_OCTET_STREAM_ATTACHMENT", Description: "Message has a generic binary attachment", Polarity: SpamRule, MaxScore: 1.3, Count: true}, evaluate: func(c *ruleContext) float64 {
		return capCount(attachmentMatches(c.message.AttachmentTypes, map[string]bool{"application/octet-stream": true}), 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_SHORT_WITH_LINK", Description: "Very short body contains a web link", Polarity: SpamRule, MaxScore: 1.3}, evaluate: func(c *ruleContext) float64 {
		return boolean(len(c.bodyTokens) > 0 && len(c.bodyTokens) < 12 && len(c.urls) > 0)
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_REPLY_FORWARD", Description: "Subject is marked as a reply or forward", Polarity: HamRule, MaxScore: 1.8}, evaluate: func(c *ruleContext) float64 {
		return boolean(startsWithToken(c.subjectTokens, "re") || startsWithToken(c.subjectTokens, "fw") || startsWithToken(c.subjectTokens, "fwd"))
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_MAILING_LIST_TAG", Description: "Subject begins with a bracketed mailing-list tag", Polarity: HamRule, MaxScore: 1.2}, evaluate: func(c *ruleContext) float64 {
		subject := strings.TrimSpace(c.subject)
		return boolean(strings.HasPrefix(subject, "[") && strings.Contains(subject, "]"))
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_MEETING_SCHEDULE", Description: "Subject concerns a meeting or schedule", Polarity: HamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "meeting notes", "meeting agenda", "project meeting", "team meeting", "schedule update", "calendar invitation")
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_RECEIPT_ORDER", Description: "Subject identifies an order, receipt, or shipment", Polarity: HamRule, MaxScore: 1.5, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "order confirmation", "order receipt", "payment receipt", "shipping update", "delivery update")
	}},
	{RuleDefinition: RuleDefinition{Name: "SUBJECT_NEWSLETTER_UPDATE", Description: "Subject identifies a periodic or community update", Polarity: HamRule, MaxScore: 1.1, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.subjectTokens, 2, "weekly newsletter", "monthly newsletter", "community newsletter", "member update", "project update", "release notes")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_MEETING_PROJECT", Description: "Body contains work meeting and project language", Polarity: HamRule, MaxScore: 2.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "meeting notes", "meeting agenda", "action items", "project plan", "project review", "team meeting", "please review")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_SCHEDULE_CALENDAR", Description: "Body contains schedule or calendar coordination", Polarity: HamRule, MaxScore: 1.7, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "calendar invite", "calendar invitation", "scheduled for", "rescheduled to", "see you tomorrow", "available on", "room change")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_TECHNICAL_DISCUSSION", Description: "Body contains authored engineering discussion phrases", Polarity: HamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "database migration", "pull request", "code review", "test failure", "build server", "release branch", "technical discussion")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_PATCH_SOURCE_CODE", Description: "Body discusses patches or source code", Polarity: HamRule, MaxScore: 1.7, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "source code", "apply the patch", "attached patch", "commit message", "bug report", "stack trace")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_FAMILY_SOCIAL", Description: "Body contains personal or family planning phrases", Polarity: HamRule, MaxScore: 1.5, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "family dinner", "happy birthday", "see you tonight", "see you this weekend", "love you", "school pickup")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_POLITE_SIGNOFF", Description: "Body contains a conventional personal signoff", Polarity: HamRule, MaxScore: 1.0, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 2, "best regards", "kind regards", "many thanks", "thanks again", "sincerely yours")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_NEWSLETTER_PREFERENCES", Description: "Body offers normal newsletter preference management", Polarity: HamRule, MaxScore: 1.1, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 2, "manage preferences", "update your preferences", "email preferences", "subscription preferences", "privacy policy")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_SHIPPING_RECEIPT", Description: "Body describes a concrete order or shipment", Polarity: HamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "order number", "order confirmation", "payment receipt", "your receipt", "tracking number", "has shipped", "delivery date")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_NO_ACTION_REQUIRED", Description: "Body explicitly says no recipient action is required", Polarity: HamRule, MaxScore: 1.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 2, "no action is required", "no action required", "for your records", "for your information")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_REPLY_QUOTATION", Description: "Body contains common quoted-reply markers", Polarity: HamRule, MaxScore: 1.3, Count: true}, evaluate: func(c *ruleContext) float64 {
		return combinedPhraseCount(c, 2, "wrote", "original message", "forwarded message", "on behalf of")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_OPEN_SOURCE", Description: "Body contains open-source development language", Polarity: HamRule, MaxScore: 1.6, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "open source", "mailing list", "software release", "documentation update", "license agreement")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_ACADEMIC", Description: "Body contains academic discussion language", Polarity: HamRule, MaxScore: 1.3, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "research paper", "conference paper", "course syllabus", "office hours", "student project", "peer review")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_TRAVEL_ITINERARY", Description: "Body contains concrete travel itinerary language", Polarity: HamRule, MaxScore: 1.4, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "flight number", "hotel reservation", "travel itinerary", "departure time", "arrival time", "booking confirmation")
	}},
	{RuleDefinition: RuleDefinition{Name: "BODY_COMMUNITY_EVENT", Description: "Body describes community events or volunteers", Polarity: HamRule, MaxScore: 1.4, Count: true}, evaluate: func(c *ruleContext) float64 {
		return phraseCount(c.bodyTokens, 3, "community event", "community meeting", "meeting minutes", "our volunteers", "members are invited", "scheduled event")
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_PLAIN_TEXT", Description: "Message is plain text without an HTML alternative", Polarity: HamRule, MaxScore: 0.9}, evaluate: func(c *ruleContext) float64 {
		return boolean(!c.message.HTML && normalizeMIME(c.message.MIMEType) == "text/plain")
	}},
	{RuleDefinition: RuleDefinition{Name: "MIME_DOCUMENT_ATTACHMENT", Description: "Message declares a document attachment", Polarity: HamRule, MaxScore: 0.8, Count: true}, evaluate: func(c *ruleContext) float64 {
		return capCount(attachmentMatches(c.message.AttachmentTypes, documentMIMEs), 3)
	}},
	{RuleDefinition: RuleDefinition{Name: "HEADER_SINGLE_RECIPIENT", Description: "Message has exactly one visible recipient", Polarity: HamRule, MaxScore: 0.6}, evaluate: func(c *ruleContext) float64 { return boolean(messageRecipientCount(c) == 1) }},
	{RuleDefinition: RuleDefinition{Name: "URI_SINGLE_HTTPS", Description: "Message contains exactly one HTTPS link", Polarity: HamRule, MaxScore: 0.5}, evaluate: func(c *ruleContext) float64 { return boolean(len(c.urls) == 1 && c.urls[0].Scheme == "https") }},
	{RuleDefinition: RuleDefinition{Name: "BODY_LONG_PROSE", Description: "Body contains at least 250 words of prose", Polarity: HamRule, MaxScore: 0.8}, evaluate: func(c *ruleContext) float64 { return boolean(len(c.bodyTokens) >= 250) }},
}

var (
	executableMIMEs = map[string]bool{
		"application/x-msdownload": true, "application/x-dosexec": true,
		"application/x-executable": true, "application/x-sh": true,
		"application/javascript": true, "text/javascript": true,
	}
	archiveMIMEs = map[string]bool{
		"application/zip": true, "application/x-zip-compressed": true,
		"application/x-rar-compressed": true, "application/x-7z-compressed": true,
		"application/gzip": true, "application/x-tar": true,
	}
	documentMIMEs = map[string]bool{
		"application/pdf": true, "text/plain": true, "text/csv": true,
		"application/msword": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       true,
	}
)

func init() {
	if len(authoredRules) > 128 {
		panic("named spam-rule table exceeds 128 entries")
	}
	seen := make(map[string]bool, len(authoredRules))
	for index := range authoredRules {
		authoredRules[index].Index = uint32(index)
		if authoredRules[index].Name == "" || seen[authoredRules[index].Name] {
			panic("invalid or duplicate named spam rule")
		}
		if authoredRules[index].Polarity != SpamRule && authoredRules[index].Polarity != HamRule {
			panic("named spam rule has no polarity")
		}
		seen[authoredRules[index].Name] = true
	}
}

// RuleDefinitions returns the complete stable rule manifest.
func RuleDefinitions() []RuleDefinition {
	result := make([]RuleDefinition, len(authoredRules))
	for index := range authoredRules {
		result[index] = authoredRules[index].RuleDefinition
	}
	return result
}

func RuleCount() int { return len(authoredRules) }

// ExtractFeatures evaluates only the fixed authored rule table. dimension is
// retained for compatibility with the prior model API and must be a power of
// two large enough to hold every stable rule ID.
func ExtractFeatures(message Message, dimension uint32) ([]Feature, error) {
	if dimension == 0 || dimension&(dimension-1) != 0 || dimension < uint32(len(authoredRules)) {
		return nil, ErrInvalidDimension
	}
	context := buildRuleContext(message)
	// Rule evaluators refer to this immutable snapshot. ExtractFeatures itself is
	// called concurrently at runtime, so evaluators that need message metadata
	// receive it through a package-local serialized helper.
	return evaluateRules(context), nil
}

// ExtractCountFeatures now aliases the same named-rule vector. It is retained
// only to avoid breaking callers while the hashed baseline is removed.
func ExtractCountFeatures(message Message, dimension uint32) ([]Feature, error) {
	return ExtractFeatures(message, dimension)
}

func evaluateRules(context *ruleContext) []Feature {
	result := make([]Feature, 0, 24)
	for _, rule := range authoredRules {
		value := rule.evaluate(context)
		if value > 0 {
			result = append(result, Feature{Index: rule.Index, Name: rule.Name, Value: value})
		}
	}
	return result
}

func buildRuleContext(message Message) *ruleContext {
	subject := truncateUTF8(message.Subject, MaxBodyBytes)
	body := truncateUTF8(message.Body, MaxBodyBytes)
	from := strings.TrimSpace(message.From)
	_, fromErr := mail.ParseAddress(from)
	return &ruleContext{
		message: message,
		subject: subject, body: body,
		subjectTokens: tokenize(subject), bodyTokens: tokenize(body),
		fromEmpty: from == "", fromValid: from != "" && fromErr == nil,
		urls: extractURLs(subject+" "+body, maxURLs),
	}
}

func tokenize(value string) []string {
	value = truncateUTF8(value, MaxBodyBytes)
	result := make([]string, 0, len(value)/6)
	var token []rune
	flush := func() {
		if len(token) > 0 {
			result = append(result, string(token))
		}
		token = token[:0]
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if len(token) < 48 {
				token = append(token, r)
			}
			continue
		}
		flush()
	}
	flush()
	return result
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func normalizeMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func attachmentMatches(values []string, accepted map[string]bool) int {
	count := 0
	for _, value := range values {
		if accepted[normalizeMIME(value)] {
			count++
		}
	}
	return count
}

func extractURLs(value string, limit int) []*url.URL {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("<>\"'()[]{}", r)
	})
	result := make([]*url.URL, 0, limit)
	for _, field := range fields {
		trimmed := strings.Trim(field, ".,;:!?\"'")
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		parsed, err := url.Parse(trimmed)
		if err == nil && parsed.Hostname() != "" {
			result = append(result, parsed)
		}
		if len(result) == limit {
			break
		}
	}
	return result
}

// URLHosts is retained for callers that need bounded URI metadata. Host names
// are never converted into model features.
func URLHosts(value string, limit int) []string {
	seen := make(map[string]bool)
	var result []string
	for _, parsed := range extractURLs(value, limit) {
		host := strings.ToLower(parsed.Hostname())
		if !seen[host] {
			seen[host] = true
			result = append(result, host)
		}
	}
	sort.Strings(result)
	return result
}

func letterCount(value string) int {
	count := 0
	for _, r := range value {
		if unicode.IsLetter(r) {
			count++
		}
	}
	return count
}

func uppercaseRatio(value string) float64 {
	letters, uppercase := 0, 0
	for _, r := range value {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			uppercase++
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(uppercase) / float64(letters)
}

func currencyAmountCount(value string) int {
	count := 0
	runes := []rune(value)
	for index, r := range runes {
		if !strings.ContainsRune("$€£¥", r) {
			continue
		}
		for next := index + 1; next < len(runes) && next <= index+3; next++ {
			if unicode.IsDigit(runes[next]) {
				count++
				break
			}
			if !unicode.IsSpace(runes[next]) {
				break
			}
		}
	}
	return count
}

func hasCurrencyAmount(value string) bool { return currencyAmountCount(value) > 0 }

func startsWithToken(tokens []string, token string) bool {
	return len(tokens) > 0 && tokens[0] == token
}

// Recipient count is stored per extraction to keep authored evaluators small.
// A context without the associated message is a programming error.
func messageRecipientCount(context *ruleContext) int {
	return len(context.message.To)
}

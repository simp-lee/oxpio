package recommend

const stopwordSetVersion = "oxpio-related-stopwords-v1"

var englishStopwordsV1 = map[string]struct{}{
	"about": {}, "above": {}, "after": {}, "again": {}, "against": {}, "all": {},
	"am": {}, "an": {}, "and": {}, "any": {}, "are": {}, "as": {}, "at": {},
	"be": {}, "because": {}, "been": {}, "before": {}, "being": {}, "below": {},
	"between": {}, "both": {}, "but": {}, "by": {}, "can": {}, "could": {},
	"did": {}, "do": {}, "does": {}, "doing": {}, "down": {}, "during": {},
	"each": {}, "few": {}, "for": {}, "from": {}, "further": {}, "had": {},
	"has": {}, "have": {}, "having": {}, "he": {}, "her": {}, "here": {},
	"hers": {}, "herself": {}, "him": {}, "himself": {}, "his": {}, "how": {},
	"if": {}, "in": {}, "into": {}, "is": {}, "it": {}, "its": {}, "itself": {},
	"just": {}, "me": {}, "more": {}, "most": {}, "my": {}, "myself": {},
	"no": {}, "nor": {}, "not": {}, "now": {}, "of": {}, "off": {}, "on": {},
	"once": {}, "only": {}, "or": {}, "other": {}, "our": {}, "ours": {},
	"ourselves": {}, "out": {}, "over": {}, "own": {}, "same": {}, "she": {},
	"should": {}, "so": {}, "some": {}, "such": {}, "than": {}, "that": {},
	"the": {}, "their": {}, "theirs": {}, "them": {}, "themselves": {},
	"then": {}, "there": {}, "these": {}, "they": {}, "this": {}, "those": {},
	"through": {}, "to": {}, "too": {}, "under": {}, "until": {}, "up": {},
	"very": {}, "was": {}, "we": {}, "were": {}, "what": {}, "when": {},
	"where": {}, "which": {}, "while": {}, "who": {}, "whom": {}, "why": {},
	"will": {}, "with": {}, "would": {}, "you": {}, "your": {}, "yours": {},
	"yourself": {}, "yourselves": {},
}

var supplementalStopwordsV1 = map[string]struct{}{
	"一个": {}, "一种": {}, "以及": {}, "他们": {}, "你们": {}, "但是": {},
	"关于": {}, "其中": {}, "因此": {}, "如果": {}, "并且": {}, "我们": {},
	"或者": {}, "所有": {}, "这些": {}, "这个": {}, "那么": {}, "那些": {},
	"那个": {}, "然而": {},
}

func isOXPIOStopword(token string) bool {
	if _, ok := englishStopwordsV1[token]; ok {
		return true
	}
	_, ok := supplementalStopwordsV1[token]
	return ok
}

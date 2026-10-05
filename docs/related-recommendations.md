# Related recommendation acceptance contract

Status: normative for the production related-article list. The values and rules
below are not user-configurable. Changing a formula, threshold, resource,
corpus boundary, candidate source, or tie-break rule is a contract change and
requires this document and the corresponding behavioral tests to change in the
same revision.

## Production corpus and candidates

A recommendation corpus contains every published ordinary article (`doc`,
`post`, and `page`) in exactly one recommendation partition. Sections
(`_index.md`), unpublished articles, and descendants hidden by an unpublished
section are excluded as sources, candidates, and DF/IDF inputs.

The partition key is the article's exact `VersionID`:

- all unversioned articles form one partition;
- each configured version entry forms a separate partition;
- term DF, tag DF, IDF, and candidate generation use only that partition;
- an article is never recommended across a version boundary.

For each source, the candidate set is the union of:

1. documents in postings for one of the source's retained terms;
2. same-partition documents connected in either direction by the source-only
   link graph; and
3. same-partition documents sharing a retained normalized tag.

The source-only graph includes successfully resolved, directly authored
Markdown links, wikilinks, and non-image note embeds to published articles.
Self-links, unresolved/private targets, image embeds, and links inherited from
expanded embed content are excluded. There is no separate pre-ranking cap on
term, tag, or link candidates: at most the other `N-1` documents in the
partition can be touched. The source itself and candidates with final score
`<= 0` are removed.

## Semantic input and tokenizer

Each article is represented by four source fields: frontmatter title, aliases,
source-AST heading text, and source-AST body text. Heading subtrees do not also
enter the body. Code blocks, math, hashtags, raw HTML blocks, link reference
definitions, note embeds, and expanded embed content are excluded. Visible
link text, inline code, ordinary visible text, and explicit image alt text are
included. Fields are tokenized independently.

Tokenization is fixed as follows:

1. Apply Unicode NFKC, then Unicode lowercase conversion.
2. Split maximal `unicode.Han` spans with the embedded read-only accurate
   DAG/HMM segmenter (`Cut(span, true)`); search-mode subwords are not emitted.
3. Outside Han spans, Unicode letters and numbers form tokens. `.`, `_`, `-`,
   `/`, and `:` are retained only between letters or numbers. Consecutive `+`
   or one `#` is retained only as a suffix after a letter or number. All other
   characters are boundaries.
4. Remove empty tokens, every single-Han-rune token, and members of the fixed
   Chinese, English, and supplemental stopword sets. Non-Han single-letter
   tokens remain.
5. No dynamic dictionary, second tokenizer, Japanese dictionary, or generic
   IDF resource is used. Chinese resources initialize lazily only when Han text
   is encountered.

Examples: `Ｎｏｄｅ．ＪＳ` becomes `node.js`; `C++ C#` becomes `c++`, `c#`;
`.NET` becomes `net`; `foo,bar` becomes `foo`, `bar`.

### Resource identity

The Chinese DAG/HMM implementation is the OXPIO adaptation of
[`go-ego/gse` v1.0.2](https://github.com/go-ego/gse/tree/v1.0.2), whose Go
module checksum is `h1:+27lYFPhQEhA9igtdOsJPRKYL/k3TwYsxBF5jr6KFv4=`. It
uses `github.com/vcaesar/cedar` v0.30.0. The HMM emission provenance is gse's
`hmm/prob_emit.go`, which identifies Jieba as its source.

The embedded resource identity is:

| Repository resource | SHA-256 |
|---|---|
| `internal/recommend/chinese/data/s_1.txt` | `2b3063ec552327520bee3c0c5819d6e131ab3db50a60b94641ec90f611c24bcd` |
| `internal/recommend/chinese/data/t_1.txt` | `2c84cef353d2daac62cc62bbeabab6b6a8866cfee8f9f88901e00ed66ed208c6` |
| `internal/recommend/chinese/data/stop_tokens.txt` | `8a05af1a224e40d06fce2081ad4d4b2c5e5c902f0a7501c0dba677ce1ee40c90` |
| `internal/recommend/chinese/hmm_model.go` | `a85802ff04319e4f8043b7ae5c9442ca62747f3a3e6d6d731ab058a3678eed58` |

The English and supplemental stopword set identity is
`oxpio-related-stopwords-v1`; its membership is defined by
`internal/recommend/stopwords.go`. The canonical membership hash sorts the
UTF-8 words lexically, appends LF after every word, and hashes the resulting
bytes:

| Stopword membership | SHA-256 |
|---|---|
| `englishStopwordsV1` (124 words) | `5f75c34b49090d105f64574def73b416c09734108c11297517b22e2b52b7e6fa` |
| `supplementalStopwordsV1` (20 words) | `30c744ed9a2f87c5dcedb3a49c5471db6517189bc9cbd60dd881fdf77ca17af2` |

Resource provenance, retained components, and licenses are inventoried in
[`internal/recommend/chinese/THIRD_PARTY.md`](../internal/recommend/chinese/THIRD_PARTY.md).
Changing any listed bytes or stopword membership requires an explicit resource
or stopword-set version change.

## TF-IDF features

For term `t`, document `d`, and field count `c`:

```text
sat(c) = 0            when c = 0
sat(c) = 1 + ln(c)    when c > 0

TFw(t,d) = 3.0 * sat(title count)
          + 2.5 * sat(alias count)
          + 2.0 * sat(heading count)
          + 1.0 * sat(body count)

IDF(t) = ln(1 + (N - df(t) + 0.5) / (df(t) + 0.5))
rawWeight(t,d) = TFw(t,d) * IDF(t)
```

`N` and `df(t)` refer only to the source's recommendation partition, and one
document contributes at most one to `df(t)`. A term is eligible only when
`df(t) >= 2`. When `N >= 20`, a term is discarded only when
`df(t)/N > 0.40`; equality is retained. No ratio cutoff applies when `N < 20`.

Each document retains the best 64 raw weights. Higher weight wins; equal
weights use term lexical order ascending. After selection, a term selected by
only one document is discarded without backfill. Remaining vectors are L2
normalized and cosine is their sparse dot product. Only out-of-range rounding
excursions in `[-1e-12,0)` or `(1,1+1e-12]` are clamped to the nearest
boundary; larger excursions and non-finite values are errors.

## Content evidence gate

A topic-shaped term has at most 48 runes, contains at least two Unicode letters
or Han runes, and letters plus Han are at least half of letters plus Han plus
digits. Pure numbers, dates of the form `YYYY-M-D` (with `-`, `/`, or `.`),
`v`/`version` numeric versions, UUIDs, and hexadecimal digests of at least 16
hex characters after removing `-_:/.` are excluded. `c++` and `c#` are explicit
exceptions.

Content cosine contributes only when:

```text
cosine >= 0.05
and at least one shared term is topic-shaped
and one of:
  - at least two terms are shared; or
  - exactly one term is shared, it is topic-shaped and occurs in the title,
    aliases, or headings of both documents, and:
      * N < 20:  df(term) <= 2
      * N >= 20: df(term)/N <= 0.05
```

A cosine that fails this gate contributes zero; it cannot by itself retain a
candidate.

## Link, tag, final score, and limits

Tags are normalized with `model.NormalizeTagName` and deduplicated per article.
When `N >= 20`, a tag is discarded only when `df(tag)/N > 0.50`; equality is
retained. No tag ratio cutoff applies when `N < 20`.

```text
link(A,B) = 0.20 if A directly links to B
          + 0.20 if B directly links to A
          + 0.10 if both directions exist

tag(A,B)  = 0.15 * |tags(A) intersect tags(B)| / |tags(A) union tags(B)|
final(A,B) = eligibleContentCosine(A,B) + link(A,B) + tag(A,B)
```

An empty tag union contributes zero. Link contribution is bounded by `0.50`,
tag contribution by `0.15`, content contribution by `1.0`, and final score by
`1.65`.

| Limit | Contract |
|---|---:|
| Default `related.count` | 5 |
| Valid configured `related.count` | `1..20`, including when disabled |
| Final result count | `0` when `N < 2`; otherwise at most `min(related.count, N-1)`, and possibly lower after candidate and positive-score filtering |
| Feature count per article | 64 before singleton-selection pruning |
| Ranking workers | at most `min(GOMAXPROCS, N, 8)`; a positive requested build concurrency below that cap is used |
| Related article title links rendered per source | final result count, therefore at most 20 |
| Candidate tag links rendered per related entry | at most 8; the current fixed shell renders none |
| Pre-ranking term/tag/link candidate count | no separate cap; at most `N-1` distinct candidates |

Ranking order is final score descending, normalized published relative path
ascending, display title ascending, then stable document ID ascending.

## Acceptance evidence

The calibrated production tuple is `(maxFeatures=64,
maxTermDFRatio=0.40, minContentCosine=0.05,
maxSingleTermDFRatio=0.05)`. Its normative machine-readable record is
`internal/recommend/testdata/quality/parameters.json`, and
`TestProductionParametersMatchManifest` requires production constants to match
it.

Calibration and holdout assets are test-only and never enter a site's runtime
DF, IDF, or candidates. The quality manifest contains 40 calibration and 40
holdout documents (10 each for Simplified Chinese, Traditional Chinese,
English, and mixed text). Calibration evaluates exactly 81 tuples from the
`3 x 3 x 3 x 3` parameter grid; holdout is not used to select parameters.

The acceptance suite is:

```bash
go test ./internal/recommend/... ./internal/build -count=1
```

It checks the production tuple, complete listed resource identities, tokenizer
examples, formula and threshold boundaries, independent link/tag recall,
candidate ranking, version-partition isolation, and quality assets.

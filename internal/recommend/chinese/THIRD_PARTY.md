# Chinese tokenizer third-party inventory

This directory is maintained as part of OXPIO. It is not a vendored copy of a
complete upstream module.

## go-ego/gse

- Upstream: <https://github.com/go-ego/gse>
- Fixed source tag: `v1.0.2`
- Go module checksum: `h1:+27lYFPhQEhA9igtdOsJPRKYL/k3TwYsxBF5jr6KFv4=`
- License: Apache-2.0; retained in `LICENSE-APACHE-2.0`.
- Retained data, copied byte-for-byte from that tag:
  - `data/dict/zh/s_1.txt` as `data/s_1.txt`, SHA-256 `2b3063ec552327520bee3c0c5819d6e131ab3db50a60b94641ec90f611c24bcd`.
  - `data/dict/zh/t_1.txt` as `data/t_1.txt`, SHA-256 `2c84cef353d2daac62cc62bbeabab6b6a8866cfee8f9f88901e00ed66ed208c6`.
  - `data/dict/zh/stop_tokens.txt` as `data/stop_tokens.txt`, SHA-256 `8a05af1a224e40d06fce2081ad4d4b2c5e5c902f0a7501c0dba677ce1ee40c90`.
- Retained/adapted implementation:
  - `hmm_model.go` derives from `hmm/prob_emit.go`. The package and loader
    were changed so the emission maps are built only during OXPIO's lazy
    initialization and are never exposed or mutated afterward.
  - `hmm.go` consolidates the required behavior from `hmm/hmm_seg.go`,
    `hmm/viterbi.go`, and `hmm/prob_trans.go`. Regex dispatch, mutable model
    replacement, sorting helpers, and public general-purpose HMM APIs were
    removed; deterministic state traversal and an immutable model remain.
  - `segmenter.go` derives the exact Chinese dictionary DAG path from
    `dictionary.go`, `dict_util.go`, `dag.go`, and `gse.go`. Dynamic dictionary
    loading, search/full modes, POS, Japanese branches, global `ToLower`,
    mutation APIs, logs, and general segmentation APIs were removed. Embedded
    simplified/traditional dictionaries are parsed once, errors are returned,
    and the resulting dictionary/model are read-only.
  - `embed.go` is OXPIO-specific resource wiring and is not copied upstream.
- Excluded: the Japanese and English dictionaries, generic IDF/TF-IDF data,
  POS, TextRank, BM25/TF-IDF extractors, CRF, GONN, RPC/server, examples,
  benchmark tools, and all dynamic file-dictionary interfaces.

## Jieba HMM provenance

The header of gse `hmm/prob_emit.go` identifies its emission data as coming
from <https://github.com/fxsjy/jieba>. The corresponding upstream source is
<https://github.com/fxsjy/jieba/blob/master/jieba/finalseg/prob_emit.py>.
Jieba is MIT licensed; the upstream license text and Sun Junyi copyright are
retained in `LICENSE-JIEBA-MIT` and `hmm_model.go`. OXPIO does not import
Jieba and contains no second tokenizer path.

## vcaesar/cedar

- Upstream: <https://github.com/vcaesar/cedar>
- Fixed dependency: `v0.30.0`
- License: BSD-2-Clause; retained in `LICENSE-CEDAR-BSD-2-CLAUSE`.
- OXPIO uses cedar's public read-only lookup operations after constructing the
  trie. No cedar source file is copied or modified in this directory.

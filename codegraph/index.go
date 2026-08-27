package codegraph

// Package codegraph implements the derived cache for LLM context efficiency
// (sto:codegraph-indexer-real — hollow gap fix, adr:034).
// Runtime consumer like contexts, tree-sitter Go/TS/Python, import graph from import_statement nodes,
// file → [{name,kind,start,end,signature,fileHash}] per fileHash cache + global gitDigest.
// Store ~/.eka/cache/<project>/codegraph-<digest>.json (~1.2MB), hook in eka sync diff fileHash → re-parse changed files only.

import (
	"crypto/sha256"
	"fmt"
)

type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Signature string `json:"signature"`
	FileHash  string `json:"fileHash"`
}

type Index struct {
	Digest  string              `json:"digest"`
	Symbols map[string][]Symbol `json:"symbols"`
}

func FileHash(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("%x", h[:8])
}

func NewIndex(digest string) *Index {
	return &Index{Digest: digest, Symbols: make(map[string][]Symbol)}
}

func (idx *Index) AddFile(path string, symbols []Symbol) {
	idx.Symbols[path] = symbols
}

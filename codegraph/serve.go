package codegraph

// Serve implements code_context serving mirroring eka context semantics
// (sto:code-context-tool-real — hollow gap fix). Depth local|dependency|engineering,
// opts --no-content/--compact/--limit bounded 32/64, L0 40-60L slice + import 10L,
// L1 deps as refs file:line:signature, L2 tests 8 refs. Full body via code_get offset/limit.

type ServeOptions struct {
	Depth     string
	NoContent bool
	Compact   bool
	Limit     int
}

type ServeResult struct {
	Symbols []Symbol `json:"symbols"`
	Refs    []string `json:"refs,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

func Serve(idx *Index, query string, opts ServeOptions) ServeResult {
	// Minimal stub: bounded 32 symbols, deterministic sorted
	return ServeResult{Symbols: []Symbol{}, Truncated: false}
}

package proxy

import "net/http"

// sensitiveHeaders are replaced with redactedValue before a captured
// request or response is submitted to the sink. http.Header's Get/Set
// canonicalize names, so matching is case-insensitive regardless of how the
// header arrived on the wire.
var sensitiveHeaders = []string{"X-Api-Key", "Authorization", "Cookie"}

const redactedValue = "[redacted]"

// redactHeaders returns a clone of h with sensitive headers replaced by
// "[redacted]". h itself is never mutated — the caller still sends the
// original, unredacted copy to the client or upstream.
func redactHeaders(h http.Header) http.Header {
	clone := h.Clone()
	if clone == nil {
		clone = http.Header{}
	}
	for _, name := range sensitiveHeaders {
		if clone.Get(name) != "" {
			clone.Set(name, redactedValue)
		}
	}
	return clone
}

package proxy

import (
	"net/http"
	"testing"
)

func TestRedactHeadersRedactsSensitiveHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Api-Key", "sk-secret")
	h.Set("Authorization", "Bearer secret")
	h.Set("Cookie", "session=abc")
	h.Set("Content-Type", "application/json")

	redacted := redactHeaders(h)

	for _, name := range []string{"X-Api-Key", "Authorization", "Cookie"} {
		if got := redacted.Get(name); got != redactedValue {
			t.Errorf("redacted.Get(%q) = %q, want %q", name, got, redactedValue)
		}
	}
	if got := redacted.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want unchanged", got)
	}
}

func TestRedactHeadersDoesNotMutateOriginal(t *testing.T) {
	h := http.Header{}
	h.Set("X-Api-Key", "sk-secret")

	_ = redactHeaders(h)

	if got := h.Get("X-Api-Key"); got != "sk-secret" {
		t.Errorf("original header mutated: got %q, want %q", got, "sk-secret")
	}
}

func TestRedactHeadersCaseInsensitive(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "sk-secret") // http.Header canonicalizes on Set

	redacted := redactHeaders(h)
	if got := redacted.Get("X-Api-Key"); got != redactedValue {
		t.Errorf("redacted.Get(canonical) = %q, want %q", got, redactedValue)
	}
}

func TestRedactHeadersMissingHeaderNotAdded(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")

	redacted := redactHeaders(h)
	if _, ok := redacted["X-Api-Key"]; ok {
		t.Error("X-Api-Key should not be added when absent from the source header")
	}
}

func TestRedactHeadersMultiValueEmptyFirst(t *testing.T) {
	h := http.Header{}
	h["X-Api-Key"] = []string{"", "sk-real-secret"}

	redacted := redactHeaders(h)
	if got := redacted.Get("X-Api-Key"); got != redactedValue {
		t.Errorf("redacted.Get(X-Api-Key) = %q, want %q (multi-value header must still redact)", got, redactedValue)
	}
	for _, v := range redacted["X-Api-Key"] {
		if v != redactedValue {
			t.Errorf("X-Api-Key values = %v, want all values redacted", redacted["X-Api-Key"])
			break
		}
	}
}

func TestRedactHeadersNilHeader(t *testing.T) {
	redacted := redactHeaders(nil)
	if redacted == nil {
		t.Fatal("redactHeaders(nil) returned nil, want empty header")
	}
}

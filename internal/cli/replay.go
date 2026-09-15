package cli

import "fmt"

// Replay is a stub: the real replay driver, and the `--replay`-gated
// `POST /api/requests/{id}/replay` endpoint it talks to, land in
// br-GI-1-13. Registered in the command map now so the name resolves and
// the error is specific.
func Replay(args []string) error {
	return fmt.Errorf("not yet implemented (br-GI-1-13)")
}

package cli

import "fmt"

// Prices is a stub: real pricing-table support (cost accounting for
// InsertRequest, `lens prices` management) lands in br-GI-1-11. Registered
// in the command map now so the name resolves and the error is specific.
func Prices(args []string) error {
	return fmt.Errorf("not yet implemented (br-GI-1-11)")
}

package reconcile

import (
	"fmt"

	"github.com/luthermonson/fargo/pkg/txn"
)

// slogScope renders a short, log-friendly summary of a scope.
func slogScope(s txn.Scope) string {
	return fmt.Sprintf("keys=%d prefixes=%d namespaces=%d expandOwners=%t",
		len(s.Keys), len(s.Prefixes), len(s.Namespaces), s.ExpandOwners)
}

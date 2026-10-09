package hibd

import (
	"context"
	"fmt"

	"github.com/solev/hibernaut/internal/links"
)

// port is hibd's side of hib port add, hib port rm and hib port ls. The laptop never reads
// links.json itself: the file belongs to the host, and only hibd knows which links are open.
//
// The body belongs to the slice that owns the published-port state. It is here, with its
// dispatch entry, so that the CLI slice and the state slice can be written at the same time
// without either waiting for the other to add a case to the switch.
func (a *App) port(ctx context.Context, args []string) int {
	fmt.Fprintf(a.Stderr, "hibd port: %v\n", links.ErrLinksOff)
	return 1
}

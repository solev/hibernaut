//go:build !linux

package linkguard

import (
	"fmt"

	"github.com/solev/hibernaut/internal/links"
)

// unsupported is the backend everywhere but Linux, where there is no nftables to guard with.
type unsupported struct{}

func newBackend(string) backend { return unsupported{} }

var errNotLinux = fmt.Errorf("%w: nftables needs Linux", links.ErrGuardDown)

func (unsupported) replace(ruleset) error                       { return errNotLinux }
func (unsupported) exists(string) (bool, error)                 { return false, errNotLinux }
func (unsupported) dump(string) (tableDump, bool, error)        { return tableDump{}, false, errNotLinux }
func (unsupported) deleteTable(string) error                    { return errNotLinux }
func (unsupported) batch(string, []elemOp) error                { return errNotLinux }
func (unsupported) list(string, string) ([]element, error)      { return nil, errNotLinux }
func (unsupported) get(string, string, []byte) (element, error) { return element{}, errNotLinux }
func (unsupported) fwmarkAccept() (bool, error)                 { return false, errNotLinux }
func (unsupported) setFwmarkAccept() error                      { return errNotLinux }

package machines

import (
	"context"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hostsblock"
)

const hostsPath = "/etc/hosts"

var hostsFile = managedFile{path: hostsPath, tag: "hibd-hosts", script: casScriptFor(hostsPath), merge: hostsblock.Merge}

// WriteHosts writes block into id's /etc/hosts through envd as root, with the same
// compare-and-swap WriteBlock uses for /etc/environment. block must be one hostsblock.Render
// returned. Nil or empty removes the managed block. Lines outside it are left alone.
func WriteHosts(ctx context.Context, e engine.Engine, id string, block []byte) error {
	return hostsFile.write(ctx, e, id, block)
}

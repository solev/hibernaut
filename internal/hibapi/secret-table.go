package hibapi

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/solev/hibernaut/internal/termsafe"
)

// PrintSecrets writes a terminal-safe metadata table. Empty fields use empty.
func PrintSecrets(w io.Writer, secrets []Secret, empty string) {
	field := func(s string) string {
		if s == "" {
			s = empty
		}
		return termsafe.String(s)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tENV\tSITES\tPLACEHOLDER")
	for _, s := range secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", field(s.Name), field(s.Env),
			field(strings.Join(s.Sites, ",")), field(s.Placeholder))
	}
	tw.Flush()
}

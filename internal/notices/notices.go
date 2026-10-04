// Package notices contains the attribution shipped inside every binary.
package notices

import _ "embed"

// Text contains full dependency and bundled font notices.
//
//go:embed NOTICE.txt
var Text string

// Package brand embeds the Lerryn logo for server-rendered pages, such as
// the credential fill page, so they use the master file instead of a copy.
package brand

import _ "embed"

// Mark is logo/svg/lerryn-mark.svg, the master logo.
//
//go:embed logo/svg/lerryn-mark.svg
var Mark string

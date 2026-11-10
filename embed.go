// Package app embeds the recorder page so the binary is self-contained.
package app

import _ "embed"

//go:embed record-zoom.html
var HTML []byte

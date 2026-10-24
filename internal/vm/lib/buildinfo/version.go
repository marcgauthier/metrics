package buildinfo

import (
	"regexp"
)

// Version must be set via -ldflags '-X'
var Version string

var shortVersionRe = regexp.MustCompile(`v\d+\.\d+\.\d+(?:-enterprise)?(?:-cluster)?`)

// ShortVersion returns a shortened version
func ShortVersion() string {
	return shortVersionRe.FindString(Version)
}

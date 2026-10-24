package logger

import (
	"flag"
	"strings"

	"github.com/marcgauthier/logmetrics/internal/vm/lib/buildinfo"
	"github.com/marcgauthier/logmetrics/internal/vm/lib/flagutil"
)

func logAllFlags() {
	Infof("build version: %s", buildinfo.Version)
	Infof("command-line flags")
	flag.Visit(func(f *flag.Flag) {
		lname := strings.ToLower(f.Name)
		value := f.Value.String()
		if flagutil.IsSecretFlag(lname) {
			value = "secret"
		}
		Infof("  -%s=%q", f.Name, value)
	})
}

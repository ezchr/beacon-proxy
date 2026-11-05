package bridge

import "os"

// env reads the BEACON_<name> environment variable, falling back to the pre-rename N2R_<name>.
func env(name string) string {
	if v := os.Getenv("BEACON_" + name); v != "" {
		return v
	}
	return os.Getenv("N2R_" + name)
}

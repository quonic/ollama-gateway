package config

import (
	"flag"
)

// LoaderOptions controls how LoadWithFlags behaves.
type LoaderOptions struct {
	ConfigFile string
}

// RegisterFlags registers command-line flags into the provided FlagSet
// and returns an options struct that captures their values.
func RegisterFlags(fs *flag.FlagSet, opts *LoaderOptions) {
	fs.StringVar(&opts.ConfigFile, "config", "", "path to YAML config file")
}

// LoadWithFlags loads configuration from a file path provided via flags or
// the given defaultPath fallback.
func LoadWithFlags(opts LoaderOptions, defaultPath string) (*Config, error) {
	path := opts.ConfigFile
	if path == "" {
		path = defaultPath
	}
	return Load(path)
}

package editor

// Config format migrations. config_version in the main file names the format a
// config is in (absent = 0). Step i converts a version-i config to version i+1
// on the node tree, so comments and layout survive; Migrate stamps the new
// version after each step. -install runs them on upgrade. A nil step means
// there's no automatic conversion from that version.

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"

	"glocker/internal/config"
)

const versionKey = "config_version"

var migrations = []func(*Doc) error{
	0: func(*Doc) error { return nil }, // v1 only adds config_version itself
}

// Version is the config's config_version (0 when absent).
func (d *Doc) Version() (int, error) {
	n := mapGet(d.cfg(), versionKey)
	if n == nil {
		return 0, nil
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s %q is not a version number", versionKey, n.Value)
	}
	return v, nil
}

// setVersion updates config_version, adding it as the first key if absent.
func (d *Doc) setVersion(v int) {
	m := d.cfg()
	if n := mapGet(m, versionKey); n != nil {
		n.Value = strconv.Itoa(v)
		return
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: versionKey}
	m.Content = append([]*yaml.Node{key, intNode(v)}, m.Content...)
}

// Migrate converts the config to config.CurrentConfigVersion in memory and
// returns the version it started at. Changes then lists what to write.
func (d *Doc) Migrate() (int, error) {
	from, err := d.Version()
	if err != nil {
		return 0, err
	}
	if from > config.CurrentConfigVersion {
		return from, fmt.Errorf("config version %d is newer than this glocker (%d)", from, config.CurrentConfigVersion)
	}
	for v := from; v < config.CurrentConfigVersion; v++ {
		if v >= len(migrations) || migrations[v] == nil {
			return from, fmt.Errorf("no automatic conversion from config version %d to %d", v, v+1)
		}
		if err := migrations[v](d); err != nil {
			return from, fmt.Errorf("converting config version %d to %d: %w", v, v+1, err)
		}
		d.setVersion(v + 1)
	}
	return from, nil
}

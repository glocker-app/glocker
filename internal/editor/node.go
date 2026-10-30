package editor

// Helpers for editing a yaml.v3 node tree in place. Working on nodes rather
// than decoded structs keeps comments, key order and !include tags intact.

import (
	"strconv"

	"gopkg.in/yaml.v3"

	"glocker/internal/config"
)

const includeTag = "!include"

var weekDays = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// body returns the top-level node of a document node.
func body(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep the old value's comments on the replacement.
			old := m.Content[i+1]
			val.LineComment, val.HeadComment, val.FootComment = old.LineComment, old.HeadComment, old.FootComment
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

func mapDel(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// path walks nested mapping keys; nil if any step is missing.
func path(m *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		m = mapGet(m, k)
	}
	return m
}

// ensure returns m[key], creating it with the given kind if absent.
func ensure(m *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if n := mapGet(m, key); n != nil {
		return n
	}
	n := &yaml.Node{Kind: kind}
	mapSet(m, key, n)
	return n
}

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

func boolNode(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
}

func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}

func getStr(m *yaml.Node, key string) string {
	if n := mapGet(m, key); n != nil {
		return n.Value
	}
	return ""
}

func getBool(m *yaml.Node, key string) bool {
	b, _ := strconv.ParseBool(getStr(m, key))
	return b
}

func getInt(m *yaml.Node, key string) int {
	i, _ := strconv.Atoi(getStr(m, key))
	return i
}

// setFlag writes an omitempty-style bool: true is written, false drops the key.
func setFlag(m *yaml.Node, key string, v bool) {
	if v {
		mapSet(m, key, boolNode(true))
	} else {
		mapDel(m, key)
	}
}

func strSeq(vals []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range vals {
		n.Content = append(n.Content, strNode(v))
	}
	return n
}

func seqStrings(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		out = append(out, c.Value)
	}
	return out
}

func removeAt(seq *yaml.Node, i int) {
	seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
}

func indexOf(seq *yaml.Node, n *yaml.Node) int {
	for i, c := range seq.Content {
		if c == n {
			return i
		}
	}
	return -1
}

// windows decodes m[key] as a list of time windows.
func windows(m *yaml.Node, key string) []config.TimeWindow {
	var out []config.TimeWindow
	if n := mapGet(m, key); n != nil {
		_ = n.Decode(&out)
	}
	return out
}

func windowNode(w config.TimeWindow) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	mapSet(n, "start", strNode(w.Start))
	mapSet(n, "end", strNode(w.End))
	mapSet(n, "days", strSeq(w.Days))
	return n
}

// setWindow replaces window i of m[key], or appends when i < 0. Existing nodes
// are edited in place so their comments survive.
func setWindow(m *yaml.Node, key string, i int, w config.TimeWindow) {
	seq := ensure(m, key, yaml.SequenceNode)
	seq.Style = 0
	if i < 0 || i >= len(seq.Content) {
		seq.Content = append(seq.Content, windowNode(w))
		return
	}
	n := seq.Content[i]
	mapSet(n, "start", strNode(w.Start))
	mapSet(n, "end", strNode(w.End))
	mapSet(n, "days", strSeq(w.Days))
}

// delWindow removes window i; an emptied list drops the key entirely so the
// "no windows" default applies.
func delWindow(m *yaml.Node, key string, i int) {
	seq := mapGet(m, key)
	if seq == nil || i >= len(seq.Content) {
		return
	}
	removeAt(seq, i)
	if len(seq.Content) == 0 {
		mapDel(m, key)
	}
}

func clone(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = clone(ch)
	}
	return &c
}

// ensurePath walks nested mapping keys, creating missing mappings. Only call
// it when writing: reads use path so viewing never adds empty stanzas.
func ensurePath(m *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		m = ensure(m, k, yaml.MappingNode)
	}
	return m
}

// setWindows replaces m[key] with ws (keeping the list node and its comments);
// an empty ws drops the key.
func setWindows(m *yaml.Node, key string, ws []config.TimeWindow) {
	if len(ws) == 0 {
		mapDel(m, key)
		return
	}
	seq := ensure(m, key, yaml.SequenceNode)
	seq.Style = 0
	seq.Content = nil
	for _, w := range ws {
		seq.Content = append(seq.Content, windowNode(w))
	}
}

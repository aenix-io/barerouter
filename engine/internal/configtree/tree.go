/*
Copyright 2026 The Aenix Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package configtree holds the configuration of the appliance as the management
// API renders it in JSON. The running configuration the engine reads back, and
// the config.boot a gateway is seeded with, both become a Tree, so what a seed
// sets up and what an Apply leaves behind are checked by the same code.
package configtree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Tree is a configuration node in the shape of the API's JSON: a map for a node
// with children, a string for a leaf with one value, a list of strings for a
// leaf with several, and an empty map for a node with neither, such as a
// valueless leaf or the key of a tag node with nothing under it.
type Tree = map[string]any

// Decode reads the JSON the API renders a configuration in. null is the empty
// configuration.
func Decode(raw []byte) (Tree, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Tree{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("the configuration is not JSON: %w", err)
	}
	switch t := v.(type) {
	case nil:
		return Tree{}, nil
	case map[string]any:
		return t, nil
	}
	return nil, fmt.Errorf("the configuration is a %T, not a JSON object", v)
}

// Get returns the node at path.
func Get(t Tree, path ...string) (any, bool) {
	var cur any = t
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Node returns the node at path when it is a node and not a leaf with values.
func Node(t Tree, path ...string) (Tree, bool) {
	v, ok := Get(t, path...)
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// Values returns the values of the leaf at path, none when there is no such leaf.
func Values(t Tree, path ...string) []string {
	v, ok := Get(t, path...)
	if !ok {
		return nil
	}
	return values(v)
}

// Value returns the value of the leaf at path when it has exactly one.
func Value(t Tree, path ...string) string {
	if vs := Values(t, path...); len(vs) == 1 {
		return vs[0]
	}
	return ""
}

func values(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, scalar(e))
		}
		return out
	case map[string]any, nil:
		return nil
	}
	return []string{scalar(v)}
}

func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprint(v)
}

// Leaf is one value at one path. A node with no value and no children is a Leaf
// with an empty Value, the same as a set operation that carries none.
type Leaf struct {
	Path  []string
	Value string
}

// Key is a string that identifies the leaf, for comparing sets of them.
func (l Leaf) Key() string { return strings.Join(l.Path, "\x1f") + "\x1e" + l.Value }

// Leaves flattens v, the node at path prefix, into its leaves.
func Leaves(v any, prefix []string) []Leaf {
	var out []Leaf
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch t := v.(type) {
		case map[string]any:
			if len(t) == 0 {
				out = append(out, Leaf{Path: slices.Clone(path)})
				return
			}
			for _, k := range slices.Sorted(maps.Keys(t)) {
				walk(t[k], append(path, k))
			}
		case nil:
			out = append(out, Leaf{Path: slices.Clone(path)})
		default:
			for _, s := range values(t) {
				out = append(out, Leaf{Path: slices.Clone(path), Value: s})
			}
		}
	}
	walk(v, slices.Clone(prefix))
	return out
}

// Set adds value at path, creating the nodes on the way. A leaf that has a value
// already gets another, unless it has this one: the tree does not know which
// leaves take one value and which take several, and nothing that uses Set sets a
// one-value leaf twice. An empty value creates the node with nothing under it.
func Set(t Tree, path []string, value string) error {
	if len(path) == 0 {
		return fmt.Errorf("set without a path")
	}
	parent := t
	for i, p := range path[:len(path)-1] {
		next, ok := parent[p]
		if !ok {
			child := Tree{}
			parent[p] = child
			parent = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is a leaf, not a node", strings.Join(path[:i+1], " "))
		}
		parent = child
	}
	last := path[len(path)-1]
	cur, exists := parent[last]
	switch {
	case value == "":
		if !exists {
			parent[last] = Tree{}
		} else if _, ok := cur.(map[string]any); !ok {
			return fmt.Errorf("%s has a value", strings.Join(path, " "))
		}
	case !exists:
		parent[last] = value
	default:
		if _, ok := cur.(map[string]any); ok {
			return fmt.Errorf("%s is a node, not a leaf", strings.Join(path, " "))
		}
		have := values(cur)
		if !slices.Contains(have, value) {
			list := make([]any, 0, len(have)+1)
			for _, h := range have {
				list = append(list, h)
			}
			parent[last] = append(list, value)
		}
	}
	return nil
}

// Delete removes the node at path with everything under it, and then every node
// on the way that is left with nothing under it, as the appliance does. Deleting
// a path that is not there does nothing.
func Delete(t Tree, path []string) {
	if len(path) == 0 {
		return
	}
	parent, ok := Node(t, path[:len(path)-1]...)
	if !ok {
		return
	}
	if _, ok := parent[path[len(path)-1]]; !ok {
		return
	}
	delete(parent, path[len(path)-1])
	if len(parent) == 0 {
		Delete(t, path[:len(path)-1])
	}
}

// Clone returns a deep copy of t.
func Clone(t Tree) Tree {
	out := make(Tree, len(t))
	for k, v := range t {
		switch c := v.(type) {
		case map[string]any:
			out[k] = Clone(c)
		case []any:
			out[k] = slices.Clone(c)
		default:
			out[k] = v
		}
	}
	return out
}

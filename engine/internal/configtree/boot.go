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

package configtree

import (
	"fmt"
	"strings"
)

// ParseBoot reads a config.boot in the form the appliance saves it in: one node
// per line, `name {` or `name tag {` opening a node, `}` closing it, `name value`
// or `name "value"` for a leaf, a bare `name` for a valueless leaf, and `//` or
// `/* */` comments, such as the version footer. It is strict about the shape,
// because a config.boot the appliance cannot read boots the gateway on its baked
// default instead, which is reachable by nobody.
func ParseBoot(text string) (Tree, error) {
	root := Tree{}
	stack := [][]string{nil}
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		at := func(format string, args ...any) error {
			return fmt.Errorf("config.boot line %d: %s", n+1, fmt.Sprintf(format, args...))
		}
		switch {
		case line == "", strings.HasPrefix(line, "//"):
			continue
		case strings.HasPrefix(line, "/*"):
			if !strings.HasSuffix(line, "*/") {
				return nil, at("a comment that does not end on its line")
			}
			continue
		case line == "}":
			if len(stack) == 1 {
				return nil, at("a } that closes nothing")
			}
			stack = stack[:len(stack)-1]
			continue
		}

		parent := stack[len(stack)-1]
		if head, ok := strings.CutSuffix(line, "{"); ok {
			words, err := split(strings.TrimSpace(head))
			if err != nil {
				return nil, at("%v", err)
			}
			if len(words) == 0 || len(words) > 2 {
				return nil, at("a node is opened by a name and at most one tag")
			}
			path := append(append([]string{}, parent...), words...)
			if err := Set(root, path, ""); err != nil {
				return nil, at("%v", err)
			}
			stack = append(stack, path)
			continue
		}

		words, err := split(line)
		if err != nil {
			return nil, at("%v", err)
		}
		if len(words) > 2 {
			return nil, at("a leaf is a name and at most one value")
		}
		path := append(append([]string{}, parent...), words[0])
		value := ""
		if len(words) == 2 {
			value = words[1]
			if value == "" {
				return nil, at("an empty value")
			}
		}
		if err := Set(root, path, value); err != nil {
			return nil, at("%v", err)
		}
	}
	if len(stack) != 1 {
		return nil, fmt.Errorf("config.boot: %d nodes are not closed", len(stack)-1)
	}
	return root, nil
}

// split breaks a line into words: runs of non-space, or double-quoted strings
// in which \" and \\ stand for a quote and a backslash.
func split(line string) ([]string, error) {
	var words []string
	for i := 0; i < len(line); {
		switch line[i] {
		case ' ', '\t':
			i++
		case '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(line) && line[j] != '"'; j++ {
				if line[j] == '\\' && j+1 < len(line) {
					j++
				}
				b.WriteByte(line[j])
			}
			if j == len(line) {
				return nil, fmt.Errorf("a quote that is not closed")
			}
			if j+1 < len(line) && line[j+1] != ' ' && line[j+1] != '\t' {
				return nil, fmt.Errorf("text right after a closing quote")
			}
			words = append(words, b.String())
			i = j + 1
		default:
			j := i
			for j < len(line) && line[j] != ' ' && line[j] != '\t' {
				if line[j] == '"' || line[j] == '{' || line[j] == '}' {
					return nil, fmt.Errorf("%q inside a bare word", line[j])
				}
				j++
			}
			words = append(words, line[i:j])
			i = j
		}
	}
	return words, nil
}

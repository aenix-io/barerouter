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

package configtree_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aenix-io/barerouter/engine/internal/configtree"
)

const boot = `firewall {
    ipv4 {
        input {
            filter {
                default-action "drop"
                rule 5 {
                    action "accept"
                    state "established"
                    state "related"
                }
                rule 1 {
                    ipsec {
                        match-ipsec-in
                    }
                }
            }
        }
    }
}
interfaces {
    ethernet eth0 {
        address dhcp
        description "pod network \"uplink\""
    }
    loopback lo {
    }
}
/* a comment the appliance keeps */
system {
    host-name "gw"
}


// Warning: Do not remove the following line.
// vyos-config-version: "bgp@8:system@33"
// Release version: 0.1.1
`

func TestParseBoot(t *testing.T) {
	t.Parallel()
	tree, err := configtree.ParseBoot(boot)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		path []string
		want []string
	}{
		{[]string{"firewall", "ipv4", "input", "filter", "default-action"}, []string{"drop"}},
		{[]string{"firewall", "ipv4", "input", "filter", "rule", "5", "state"}, []string{"established", "related"}},
		{[]string{"interfaces", "ethernet", "eth0", "address"}, []string{"dhcp"}},
		{[]string{"interfaces", "ethernet", "eth0", "description"}, []string{`pod network "uplink"`}},
		{[]string{"system", "host-name"}, []string{"gw"}},
	}
	for _, c := range checks {
		if got := configtree.Values(tree, c.path...); !slices.Equal(got, c.want) {
			t.Errorf("%s = %q, want %q", strings.Join(c.path, " "), got, c.want)
		}
	}
	for _, empty := range [][]string{
		{"firewall", "ipv4", "input", "filter", "rule", "1", "ipsec", "match-ipsec-in"},
		{"interfaces", "loopback", "lo"},
	} {
		if n, ok := configtree.Node(tree, empty...); !ok || len(n) != 0 {
			t.Errorf("%s = %v, want a node with nothing under it", strings.Join(empty, " "), n)
		}
	}
}

func TestParseBootRefusesWhatTheApplianceCannotRead(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"unclosed node":     "system {\n    host-name gw\n",
		"stray brace":       "system {\n}\n}\n",
		"unclosed quote":    "system {\n    host-name \"gw\n}\n",
		"two values":        "system {\n    host-name gw other\n}\n",
		"text after quote":  "system {\n    host-name \"gw\"x\n}\n",
		"quote in a word":   "system {\n    host-name g\"w\n}\n",
		"leaf then node":    "system {\n    host-name gw\n    host-name {\n    }\n}\n",
		"node then leaf":    "system {\n    login {\n    }\n    login x\n}\n",
		"three-word header": "interfaces {\n    ethernet eth0 extra {\n    }\n}\n",
		"open comment":      "/* never closed\nsystem {\n}\n",
	} {
		if _, err := configtree.ParseBoot(text); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestDecodeKeepsTheShapeOfTheAPI(t *testing.T) {
	t.Parallel()
	raw := `{"firewall": {"ipv4": {"name": {"T": {"default-action": "drop", "rule": {` +
		`"5": {"state": ["established", "related"]}, "10": {"ipsec": {"match-ipsec-in": {}}}}}}}}}`
	tree, err := configtree.Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := keys(configtree.Leaves(tree, nil))
	want := []string{
		"firewall ipv4 name T default-action = drop",
		"firewall ipv4 name T rule 10 ipsec match-ipsec-in = ",
		"firewall ipv4 name T rule 5 state = established",
		"firewall ipv4 name T rule 5 state = related",
	}
	if !slices.Equal(got, want) {
		t.Errorf("leaves:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, raw := range []string{"", "null"} {
		if tree, err := configtree.Decode([]byte(raw)); err != nil || len(tree) != 0 {
			t.Errorf("Decode(%q) = %v, %v, want an empty tree", raw, tree, err)
		}
	}
	if _, err := configtree.Decode([]byte(`"text"`)); err == nil {
		t.Error("a JSON string decoded as a configuration")
	}
}

// Set and Delete are how the simulated appliance applies a commit, so a tree
// built by them must render as the API does.
func TestSetAndDelete(t *testing.T) {
	t.Parallel()
	tree := configtree.Tree{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(configtree.Set(tree, []string{"vpn", "ipsec", "authentication", "psk", "p", "id"}, "203.0.113.10"))
	must(configtree.Set(tree, []string{"vpn", "ipsec", "authentication", "psk", "p", "id"}, "198.51.100.1"))
	must(configtree.Set(tree, []string{"vpn", "ipsec", "authentication", "psk", "p", "id"}, "198.51.100.1"))
	must(configtree.Set(tree, []string{"protocols", "static", "route", "192.0.2.0/24", "next-hop", "10.0.0.1"}, ""))
	must(configtree.Set(tree, []string{"protocols", "bgp", "system-as"}, "65001"))
	raw, _ := json.Marshal(tree)
	want := `{"protocols":{"bgp":{"system-as":"65001"},"static":{"route":{"192.0.2.0/24":{"next-hop":{"10.0.0.1":{}}}}}},` +
		`"vpn":{"ipsec":{"authentication":{"psk":{"p":{"id":["203.0.113.10","198.51.100.1"]}}}}}}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
	if err := configtree.Set(tree, []string{"protocols", "bgp", "system-as", "x"}, "1"); err == nil {
		t.Error("a node was set under a leaf")
	}
	if err := configtree.Set(tree, []string{"protocols", "bgp"}, "1"); err == nil {
		t.Error("a value was set on a node")
	}

	configtree.Delete(tree, []string{"vpn", "ipsec"})
	configtree.Delete(tree, []string{"protocols", "static", "route", "203.0.113.0/24"})
	if _, ok := configtree.Get(tree, "vpn"); ok {
		t.Error("deleting the only child left its parent behind")
	}
	if _, ok := configtree.Get(tree, "protocols", "static", "route", "192.0.2.0/24"); !ok {
		t.Error("deleting what is not there removed something")
	}
}

func TestCloneIsDeep(t *testing.T) {
	t.Parallel()
	tree := configtree.Tree{}
	_ = configtree.Set(tree, []string{"a", "b"}, "1")
	_ = configtree.Set(tree, []string{"a", "b"}, "2")
	clone := configtree.Clone(tree)
	_ = configtree.Set(clone, []string{"a", "b"}, "3")
	configtree.Delete(clone, []string{"a"})
	if got := configtree.Values(tree, "a", "b"); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("the original changed with its clone: %v", got)
	}
}

func keys(leaves []configtree.Leaf) []string {
	out := make([]string, 0, len(leaves))
	for _, l := range leaves {
		out = append(out, strings.Join(l.Path, " ")+" = "+l.Value)
	}
	slices.Sort(out)
	return out
}

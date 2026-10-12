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

package engine

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxSecrets bounds how many secrets an engine remembers, oldest dropped first:
// enough for every key of every gateway an operator drives, and a bound on the
// memory a long life of rotations takes.
const maxSecrets = 4096

// maxMessage bounds an error message, cut only after it is redacted, so that
// no prefix of a secret survives the cut.
const maxMessage = 1024

// secretSet is what an engine knows to be secret.
type secretSet struct {
	mu    sync.Mutex
	order []string
	known map[string]bool
}

func newSecretSet() *secretSet { return &secretSet{known: map[string]bool{}} }

func (s *secretSet) add(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range values {
		if v == "" || s.known[v] {
			continue
		}
		s.known[v] = true
		s.order = append(s.order, v)
		if len(s.order) > maxSecrets {
			delete(s.known, s.order[0])
			s.order = s.order[1:]
		}
	}
}

func (s *secretSet) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

// with returns a redactor of what the engine knows and of own, which only this
// redactor removes: values that are not known to be secrets of a gateway, such
// as credentials that failed their check, stay out of what every other session
// redacts.
func (s *secretSet) with(own ...string) redactor { return redactor{set: s, own: own} }

// redactor removes secrets from what leaves the engine.
type redactor struct {
	set *secretSet
	own []string
}

// text returns s with every secret replaced. Longer secrets go first, so one
// that contains another is not left half exposed.
func (r redactor) text(s string) string {
	secrets := append(r.set.all(), r.own...)
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "***")
		}
	}
	return s
}

// err returns err redacted and prefixed with what failed. The result does not
// wrap err, so the text before redaction cannot be had by unwrapping; it wraps
// only the cancellation of the context, if that is the cause, so a caller can
// still tell it.
func (r redactor) err(what string, err error) error {
	msg := r.text(what + ": " + err.Error())
	if len(msg) > maxMessage {
		cut := maxMessage
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…"
	}
	out := &redactedError{msg: msg}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			out.cause = cause
		}
	}
	return out
}

type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

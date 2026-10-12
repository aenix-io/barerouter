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
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A message is redacted whole and cut after: cut first, a secret across the cut
// would leave its first characters behind, which redaction no longer matches.
func TestARedactedMessageIsCutAfterItIsRedacted(t *testing.T) {
	t.Parallel()
	const secret = "0123456789abcdefSECRET"
	set := newSecretSet()
	set.add(secret)
	prefix := strings.Repeat("x", maxMessage-len("op: ")-8)
	err := set.with().err("op", errors.New(prefix+secret+" and more"))
	if strings.Contains(err.Error(), secret[:8]) {
		t.Errorf("a prefix of the secret survived: %q", err.Error()[len(err.Error())-40:])
	}
	if len(err.Error()) > maxMessage+len("…") {
		t.Errorf("the message is %d bytes", len(err.Error()))
	}
}

// One secret inside another is not left half exposed by replacing the shorter
// first.
func TestLongerSecretsGoFirst(t *testing.T) {
	t.Parallel()
	set := newSecretSet()
	set.add("abc", "abcdef-longer")
	if got := set.with().text("key abcdef-longer"); got != "key ***" {
		t.Errorf("got %q", got)
	}
}

func TestOwnSecretsStayOwn(t *testing.T) {
	t.Parallel()
	set := newSecretSet()
	if got := set.with("mine").text("mine"); got != "***" {
		t.Errorf("got %q", got)
	}
	if got := set.with().text("mine"); got != "mine" {
		t.Errorf("a value one redactor was given reached every other: %q", got)
	}
}

func TestTheSetForgetsTheOldestPastItsBound(t *testing.T) {
	t.Parallel()
	set := newSecretSet()
	for i := range maxSecrets + 1 {
		set.add(fmt.Sprintf("secret-%05d", i))
	}
	if got := set.with().text("secret-00000"); got != "secret-00000" {
		t.Error("the oldest secret was kept past the bound")
	}
	if got := set.with().text(fmt.Sprintf("secret-%05d", maxSecrets)); got != "***" {
		t.Error("the newest secret was dropped")
	}
}

// The error does not wrap what it was made from, which unwrapping would give
// back unredacted, but a cancelled context is still one.
func TestARedactedErrorWrapsOnlyCancellation(t *testing.T) {
	t.Parallel()
	set := newSecretSet()
	set.add("psk")
	err := set.with().err("op", fmt.Errorf("refused psk: %w", context.Canceled))
	if !errors.Is(err, context.Canceled) {
		t.Error("the cancellation is lost")
	}
	if u := errors.Unwrap(err); u != context.Canceled {
		t.Errorf("unwraps to %v", u)
	}
	plain := set.with().err("op", fmt.Errorf("refused psk: %w", errors.New("psk")))
	if errors.Unwrap(plain) != nil {
		t.Error("the error wraps its unredacted cause")
	}
}

package msg

import (
	"strings"
	"sync"
	"testing"
)

// translationsMustBeComplete guards the invariant the review relied on: every
// message key exists in every supported locale, no locale has stale keys, and
// the format strings use the same number of verbs. A missing key would make
// T() return the raw key to users.
func TestTranslationsAreCompleteAndConsistent(t *testing.T) {
	if len(translations) < 2 {
		t.Fatalf("translations = %d locales, want at least zh and en", len(translations))
	}
	reference, ok := translations["zh"]
	if !ok {
		t.Fatal("zh translations missing")
	}
	for locale, table := range translations {
		if len(table) != len(reference) {
			t.Fatalf("locale %q has %d messages, want %d", locale, len(table), len(reference))
		}
		for key, wantText := range reference {
			gotText, ok := table[key]
			if !ok {
				t.Fatalf("locale %q is missing key %q", locale, key)
			}
			if strings.TrimSpace(gotText) == "" {
				t.Fatalf("locale %q key %q has an empty translation", locale, key)
			}
			if got, want := strings.Count(gotText, "%"), strings.Count(wantText, "%"); got != want {
				t.Fatalf("locale %q key %q has %d format verbs, zh has %d", locale, key, got, want)
			}
		}
		for key := range table {
			if _, ok := reference[key]; !ok {
				t.Fatalf("locale %q has unknown key %q", locale, key)
			}
		}
	}
}

// Every key must resolve to real text in every locale, including the gateway
// rejection messages (review P3-3).
func TestEveryKeyTranslates(t *testing.T) {
	keys := []string{
		CrossSiteWriteRejected, GatewayLoginRequired, AdminOnly,
		ConfigChangedOnDisk, LostPart, InternalError,
	}
	for _, locale := range []string{"zh", "en"} {
		t.Cleanup(func() { SetLocale("zh") })
		SetLocale(locale)
		for _, key := range keys {
			if got := T(key); got == key || strings.TrimSpace(got) == "" {
				t.Fatalf("T(%q) in locale %q = %q, want a real translation", key, locale, got)
			}
		}
	}
}

func TestUnknownKeyReturnsKey(t *testing.T) {
	if got := T("NoSuchMessageKey"); got != "NoSuchMessageKey" {
		t.Fatalf("T(unknown) = %q, want the key itself", got)
	}
}

// Configuration accepts locale strings such as "EN", "en-US" and "zh_CN";
// they must select the right table instead of falling back to untranslated
// Chinese or the raw key (review P3-3).
func TestSetLocaleNormalizesInput(t *testing.T) {
	t.Cleanup(func() { SetLocale("zh") })
	for input, want := range map[string]string{
		"":      "zh",
		"zh":    "zh",
		"EN":    "en",
		"en":    "en",
		"en-US": "en",
		"en_US": "en",
		" zh ":  "zh",
		"zh-CN": "zh",
		"fr":    "zh",
		"xx-YY": "zh",
	} {
		SetLocale(input)
		if got := Locale(); got != want {
			t.Fatalf("SetLocale(%q) -> Locale() = %q, want %q", input, got, want)
		}
	}
}

// Concurrent readers must not race with a locale change (checked under -race).
func TestLocaleChangeIsSafeUnderConcurrency(t *testing.T) {
	t.Cleanup(func() { SetLocale("zh") })
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			SetLocale([]string{"en", "zh", "EN", ""}[i%4])
		}
	}()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 500; j++ {
				if got := T(CrossSiteWriteRejected); got == CrossSiteWriteRejected || got == "" {
					t.Errorf("T() returned %q", got)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}

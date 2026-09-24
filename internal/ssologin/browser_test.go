package ssologin

import "testing"

// Real `lsappinfo visibleProcessList` output: the app in front first, the
// one behind it next, and so on down the stack.
const visibleList = `ASN:0x0-0x229229-"Code": ASN:0x0-0xafaafa-"iTerm2": ASN:0x0-0x224224-"Google_Chrome": ` +
	`ASN:0x0-0x47f47f-"Safari": ASN:0x0-0x2a02a-"Finder":`

func TestFirstBrowserSkipsNonBrowsers(t *testing.T) {
	bundles := map[string]string{
		"ASN:0x0-0x229229": "com.microsoft.VSCode",
		"ASN:0x0-0xafaafa": "com.googlecode.iterm2",
		"ASN:0x0-0x224224": "com.google.Chrome",
		"ASN:0x0-0x47f47f": "com.apple.Safari",
		"ASN:0x0-0x2a02a":  "com.apple.finder",
	}
	got := firstBrowser(visibleList, func(asn string) string { return bundles[asn] })
	if got != "com.google.Chrome" {
		t.Fatalf("eligió %q, esperaba com.google.Chrome (el navegador más adelante en la lista)", got)
	}
}

func TestFirstBrowserWithoutAnyBrowser(t *testing.T) {
	got := firstBrowser(visibleList, func(string) string { return "com.apple.finder" })
	if got != "" {
		t.Fatalf("eligió %q sin ningún navegador abierto, esperaba caer al navegador por defecto", got)
	}
}

func TestBundleIDPatternParsesLsappinfoLine(t *testing.T) {
	m := bundleIDPattern.FindStringSubmatch(`"CFBundleIdentifier"="com.apple.Safari"` + "\n")
	if m == nil || m[1] != "com.apple.Safari" {
		t.Fatalf("no se extrajo el bundle ID de la respuesta de lsappinfo: %v", m)
	}
}

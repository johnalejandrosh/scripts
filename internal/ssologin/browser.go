package ssologin

import (
	"os"
	"os/exec"
	"regexp"
)

// browserBundleIDs are the macOS bundle identifiers we recognise as a
// browser. Everything else the user has open — our own terminal, the
// editor, Slack — is skipped while walking the app list looking for the
// one that should get the SSO authorization page.
var browserBundleIDs = map[string]bool{
	"com.apple.Safari":                    true,
	"com.apple.SafariTechnologyPreview":   true,
	"com.google.Chrome":                   true,
	"com.google.Chrome.beta":              true,
	"com.google.Chrome.dev":               true,
	"com.google.Chrome.canary":            true,
	"org.chromium.Chromium":               true,
	"com.microsoft.edgemac":               true,
	"com.microsoft.edgemac.Beta":          true,
	"com.microsoft.edgemac.Dev":           true,
	"org.mozilla.firefox":                 true,
	"org.mozilla.firefoxdeveloperedition": true,
	"org.mozilla.nightly":                 true,
	"com.brave.Browser":                   true,
	"com.brave.Browser.beta":              true,
	"com.vivaldi.Vivaldi":                 true,
	"com.operasoftware.Opera":             true,
	"com.operasoftware.OperaGX":           true,
	"company.thebrowser.Browser":          true, // Arc
	"company.thebrowser.dia":              true, // Dia
	"com.kagi.kagimacOS":                  true, // Orion
	"app.zen-browser.zen":                 true,
	"ru.yandex.desktop.yandex-browser":    true,
}

var (
	// `lsappinfo visibleProcessList` prints the apps front to back, i.e.
	// most recently used first:
	//   ASN:0x0-0xafaafa-"iTerm2": ASN:0x0-0x47f47f-"Safari": ...
	// We only need the serial numbers, to ask about each app in turn.
	asnPattern = regexp.MustCompile(`ASN:0x[0-9a-fA-F]+-0x[0-9a-fA-F]+`)
	// `lsappinfo info -only bundleID <asn>` answers with a single line:
	//   "CFBundleIdentifier"="com.apple.Safari"
	bundleIDPattern = regexp.MustCompile(`"CFBundleIdentifier"="([^"]+)"`)
)

// lastUsedBrowser returns the bundle ID of the browser the user was on most
// recently, or "" when no browser is open (or macOS declines to say) — in
// which case the system default browser is the right thing to fall back to.
// It costs a couple of `lsappinfo` calls, so keep it off the UI thread.
func lastUsedBrowser() string {
	out, err := exec.Command("lsappinfo", "visibleProcessList").Output()
	if err != nil {
		return ""
	}
	return firstBrowser(string(out), bundleIDOf)
}

// firstBrowser walks visibleList front to back — lsappinfo lists the app in
// front first, then the one behind it, and so on — and returns the bundle
// ID of the first app bundleIDOf reports as a browser.
func firstBrowser(visibleList string, bundleIDOf func(asn string) string) string {
	for _, asn := range asnPattern.FindAllString(visibleList, -1) {
		if id := bundleIDOf(asn); browserBundleIDs[id] {
			return id
		}
	}
	return ""
}

// bundleIDOf asks macOS which app owns the application serial number asn,
// answering "" for an app that has gone away in the meantime.
func bundleIDOf(asn string) string {
	out, err := exec.Command("lsappinfo", "info", "-only", "bundleID", asn).Output()
	if err != nil {
		return ""
	}
	m := bundleIDPattern.FindSubmatch(out)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// OpenBrowser best-effort opens url in the browser the user was last using,
// falling back to the default browser. Failures are harmless: the caller
// should always also show the URL as text. It returns immediately; picking
// the browser happens in the background.
func OpenBrowser(url string) {
	go func() {
		if id := lastUsedBrowser(); id != "" {
			if exec.Command("open", "-b", id, url).Run() == nil {
				return
			}
		}
		_ = exec.Command("open", url).Start()
	}()
}

// browserEnv is the environment `aws sso login` should run with so that it
// too lands on the browser the user was last using instead of the default
// one. The AWS CLI opens its authorization page through Python's
// webbrowser module, which takes BROWSER as a command template; when the
// command is missing or fails, webbrowser just moves on to the default
// browser, so this can only improve on the old behaviour. Returns nil (i.e.
// "inherit our own environment") when no browser is open.
func browserEnv() []string {
	id := lastUsedBrowser()
	if id == "" {
		return nil
	}
	return append(os.Environ(), "BROWSER=open -b "+id+" %s")
}

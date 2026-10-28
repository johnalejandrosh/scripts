package ssologin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// NormalizeStartURL strips what the portal and its copy buttons tack onto
// the start URL ("/", "#", "#/"). The AWS CLI keys the legacy token cache on
// the exact text, so ".../start" and ".../start/#" are two separate logins
// for the very same portal.
func NormalizeStartURL(u string) string {
	u = strings.TrimSpace(u)
	for {
		trimmed := strings.TrimRight(strings.TrimSuffix(u, "#"), "/")
		if trimmed == u {
			return u
		}
		u = trimmed
	}
}

// loginKey is what decides which cached token a profile reads: the session
// name for [sso-session] profiles, the exact start URL for legacy ones.
func loginKey(p Profile) string {
	if p.Session != "" {
		return "sso-session " + p.Session
	}
	return p.StartURL
}

// LoginGroups counts how many separate `aws sso login` runs it takes to have
// every profile logged in. Anything above the number of distinct portals
// means the config mixes formats or URL spellings: see CleanSSO.
func LoginGroups(profiles []Profile) (groups, portals int) {
	keys := make(map[string]bool)
	urls := make(map[string]bool)
	for _, p := range profiles {
		keys[loginKey(p)] = true
		urls[NormalizeStartURL(p.StartURL)] = true
	}
	return len(keys), len(urls)
}

// CleanResult summarizes one CleanSSO run.
type CleanResult struct {
	Backup        string   // copy of the config before it was rewritten
	Profiles      int      // SSO profiles rewritten to the single format
	Sessions      []string // [sso-session] blocks removed
	Portals       []string // distinct start URLs left, i.e. logins needed
	CacheRemoved  int      // cached tokens and role credentials deleted
	LoginProfile  string   // a profile of the first portal, to log in with
	LogoutWarning string   // `aws sso logout` failed; the cache is gone anyway
}

// CleanSSO leaves ~/.aws/config so that one login per portal covers every
// profile: it backs the file up, rewrites every SSO profile to the legacy
// flat format (the one WriteProfile uses) with its normalized start URL,
// drops the [sso-session] blocks nothing references any more, and wipes the
// cached tokens and role credentials so no stale session survives.
func CleanSSO(ctx context.Context) (CleanResult, error) {
	var result CleanResult
	path, err := configPath()
	if err != nil {
		return result, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}

	// Revoke the tokens server side while the old config still resolves
	// them; failing here is fine, the files are deleted below either way.
	logoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := exec.CommandContext(logoutCtx, "aws", "sso", "logout").CombinedOutput()
	cancel()
	if err != nil {
		result.LogoutWarning = firstLine(string(out), err)
	}

	result.Backup = path + ".bak-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(result.Backup, data, 0o600); err != nil {
		return result, err
	}

	content, profiles, sessions := rewriteSSOConfig(string(data))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return result, err
	}
	result.Profiles, result.Sessions = len(profiles), sessions

	portals := make(map[string]string)
	for _, p := range profiles {
		if _, ok := portals[p.StartURL]; !ok {
			portals[p.StartURL] = p.Name
		}
	}
	for url := range portals {
		result.Portals = append(result.Portals, url)
	}
	sort.Strings(result.Portals)
	if len(result.Portals) > 0 {
		result.LoginProfile = portals[result.Portals[0]]
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return result, err
	}
	for _, dir := range []string{
		filepath.Join(home, ".aws", "sso", "cache"),
		filepath.Join(home, ".aws", "cli", "cache"),
	} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") &&
				os.Remove(filepath.Join(dir, e.Name())) == nil {
				result.CacheRemoved++
			}
		}
	}
	return result, nil
}

// ssoKeys are the profile fields that say where the token comes from; the
// rewrite replaces all of them with one sso_start_url/sso_region pair.
var ssoKeys = map[string]bool{
	"sso_start_url":           true,
	"sso_region":              true,
	"sso_session":             true,
	"sso_registration_scopes": true,
}

var extraBlankLines = regexp.MustCompile(`\n{3,}`)

// rewriteSSOConfig does CleanSSO's edit on the text of ~/.aws/config,
// line by line so comments, ordering and non-SSO profiles stay as they were.
// It returns the new text, the SSO profiles it rewrote (with their
// normalized start URL) and the [sso-session] blocks it removed.
func rewriteSSOConfig(content string) (string, []Profile, []string) {
	profiles := parseSSOProfiles(content)
	byName := make(map[string]Profile, len(profiles))
	for i := range profiles {
		profiles[i].StartURL = NormalizeStartURL(profiles[i].StartURL)
		profiles[i].Session = ""
		byName[profiles[i].Name] = profiles[i]
	}

	// A session stays only while a profile we don't rewrite still uses it.
	stillUsed := make(map[string]bool)
	for _, s := range parseSections(content) {
		if _, rewritten := byName[s.name]; s.kind == "profile" && !rewritten && s.kv["sso_session"] != "" {
			stillUsed[s.kv["sso_session"]] = true
		}
	}

	var b strings.Builder
	var removed []string
	skipping, rewriting := false, false
	for _, raw := range strings.SplitAfter(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			header := strings.TrimSpace(line[1 : len(line)-1])
			name := strings.TrimSpace(strings.TrimPrefix(header, "profile "))
			skipping, rewriting = false, false
			switch {
			case strings.HasPrefix(header, "sso-session "):
				session := strings.TrimSpace(strings.TrimPrefix(header, "sso-session "))
				if !stillUsed[session] {
					skipping = true
					removed = append(removed, session)
					continue
				}
			case header == "default" || strings.HasPrefix(header, "profile "):
				if p, ok := byName[name]; ok {
					rewriting = true
					b.WriteString(raw)
					if !strings.HasSuffix(raw, "\n") {
						b.WriteString("\n")
					}
					b.WriteString("sso_start_url = " + p.StartURL + "\n")
					b.WriteString("sso_region = " + p.SSORegion + "\n")
					continue
				}
			}
		}
		if skipping {
			continue
		}
		if rewriting {
			if key, _, ok := strings.Cut(line, "="); ok && ssoKeys[strings.ToLower(strings.TrimSpace(key))] {
				continue
			}
		}
		b.WriteString(raw)
	}

	out := extraBlankLines.ReplaceAllString(b.String(), "\n\n")
	return out, profiles, removed
}

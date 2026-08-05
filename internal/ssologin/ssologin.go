// Package ssologin discovers AWS IAM Identity Center (SSO) profiles from
// the user's own ~/.aws/config, drives `aws sso login`/`aws sso logout` for
// them, and can create a brand new profile end to end — device
// authorization, account/role pick, ~/.aws/config write — using only plain
// `aws sso-oidc`/`aws sso` subcommands, entirely from within our own UI
// instead of handing the terminal over to `aws configure sso`.
//
// The only thing ever written to disk here is the (non-secret) profile
// block in ~/.aws/config. Access tokens obtained along the way are kept in
// memory just long enough to list accounts/roles, then discarded — the
// actual, securely-cached SSO session is the one `aws sso login` creates
// for real under ~/.aws/sso/cache, with its own restrictive permissions.
package ssologin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Profile is one SSO-enabled profile found in ~/.aws/config, i.e. it has
// sso_account_id/sso_role_name plus a resolvable start URL and region
// (either set directly on the profile, or via a referenced [sso-session]).
type Profile struct {
	Name      string // the `aws --profile <Name>` name
	AccountID string
	RoleName  string
	StartURL  string
	SSORegion string
	CLIRegion string
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aws", "config"), nil
}

// DiscoverProfiles reads ~/.aws/config and returns every profile that looks
// SSO-enabled, sorted by name. A missing file just means "no profiles yet",
// not an error.
func DiscoverProfiles() ([]Profile, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseSSOProfiles(string(data)), nil
}

type iniSection struct {
	kind string // "profile" or "sso-session"
	name string
	kv   map[string]string
}

func parseSections(content string) []iniSection {
	var sections []iniSection
	var cur *iniSection

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			header := strings.TrimSpace(line[1 : len(line)-1])
			kind, name := "other", header
			switch {
			case header == "default":
				kind, name = "profile", "default"
			case strings.HasPrefix(header, "profile "):
				kind, name = "profile", strings.TrimSpace(strings.TrimPrefix(header, "profile "))
			case strings.HasPrefix(header, "sso-session "):
				kind, name = "sso-session", strings.TrimSpace(strings.TrimPrefix(header, "sso-session "))
			}
			sections = append(sections, iniSection{kind: kind, name: name, kv: map[string]string{}})
			cur = &sections[len(sections)-1]
			continue
		}

		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		cur.kv[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(val)
	}

	return sections
}

// parseSSOProfiles resolves each [profile ...] section against the legacy
// (sso_start_url/sso_region directly on the profile) and modern
// (sso_session = name, pointing at a [sso-session name] block) formats that
// `aws configure sso` can produce.
func parseSSOProfiles(content string) []Profile {
	sections := parseSections(content)

	ssoSessions := make(map[string]iniSection)
	for _, s := range sections {
		if s.kind == "sso-session" {
			ssoSessions[s.name] = s
		}
	}

	var out []Profile
	for _, s := range sections {
		if s.kind != "profile" {
			continue
		}
		accountID := s.kv["sso_account_id"]
		roleName := s.kv["sso_role_name"]
		if accountID == "" || roleName == "" {
			continue
		}

		startURL, ssoRegion := s.kv["sso_start_url"], s.kv["sso_region"]
		if sessionName, ok := s.kv["sso_session"]; ok {
			if sess, ok := ssoSessions[sessionName]; ok {
				startURL, ssoRegion = sess.kv["sso_start_url"], sess.kv["sso_region"]
			}
		}
		if startURL == "" || ssoRegion == "" {
			continue
		}

		out = append(out, Profile{
			Name:      s.name,
			AccountID: accountID,
			RoleName:  roleName,
			StartURL:  startURL,
			SSORegion: ssoRegion,
			CLIRegion: s.kv["region"],
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StreamCommand runs name(args...) and streams its combined stdout+stderr,
// line by line, on the returned channel, which is closed once the process
// exits; the exit result then arrives on the second channel.
func StreamCommand(ctx context.Context, name string, args ...string) (<-chan string, <-chan error) {
	lines := make(chan string, 256)
	done := make(chan error, 1)

	go func() {
		cmd := exec.CommandContext(ctx, name, args...)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			close(lines)
			done <- err
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			close(lines)
			done <- err
			return
		}
		if err := cmd.Start(); err != nil {
			close(lines)
			done <- err
			return
		}

		var wg sync.WaitGroup
		wg.Add(2)
		scan := func(r io.Reader) {
			defer wg.Done()
			sc := bufio.NewScanner(r)
			for sc.Scan() {
				lines <- sc.Text()
			}
		}
		go scan(stdout)
		go scan(stderr)

		wg.Wait()
		close(lines)
		done <- cmd.Wait()
	}()

	return lines, done
}

// Login runs `aws sso login --profile profile`, which prints (and tries to
// open in a browser) the verification URL/code and blocks until the user
// approves it there.
func Login(ctx context.Context, profile string) (<-chan string, <-chan error) {
	return StreamCommand(ctx, "aws", "sso", "login", "--profile", profile)
}

// Logout runs `aws sso logout`. The AWS CLI has no per-profile logout: this
// clears the cached SSO token for every profile at once.
func Logout(ctx context.Context) (<-chan string, <-chan error) {
	return StreamCommand(ctx, "aws", "sso", "logout")
}

// CheckStatus reports whether profile currently has a valid, non-expired
// SSO session (cheap: it doesn't trigger a login, just checks the cache).
func CheckStatus(profile string) bool {
	return exec.Command("aws", "sts", "get-caller-identity", "--profile", profile).Run() == nil
}

// --- New-profile wizard: device authorization, account/role discovery ---

// OIDCClient is what `aws sso-oidc register-client` hands back.
type OIDCClient struct {
	ClientID     string
	ClientSecret string
}

// DeviceAuthorization is what `aws sso-oidc start-device-authorization`
// hands back: the code to show the user and where they approve it.
type DeviceAuthorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	IntervalSeconds         int
}

type Account struct {
	AccountID   string
	AccountName string
	Email       string
}

type Role struct {
	RoleName  string
	AccountID string
}

func runAWSJSON(ctx context.Context, dst any, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "aws", append(args, "--output", "json")...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return errors.New(firstLine(stderr.String(), err))
	}
	return json.Unmarshal(out, dst)
}

// awsErrorName extracts the exception name AWS CLI prints as
// "An error occurred (ExceptionName) when calling ...".
func awsErrorName(stderrText string) string {
	start := strings.Index(stderrText, "(")
	end := strings.Index(stderrText, ")")
	if start >= 0 && end > start {
		return stderrText[start+1 : end]
	}
	return ""
}

func firstLine(stderrText string, fallback error) string {
	for _, line := range strings.Split(stderrText, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return fallback.Error()
}

// RegisterClient registers this app as a public OAuth client with IAM
// Identity Center. Safe to call every time: it's a cheap, idempotent-ish
// registration, not tied to any particular user.
func RegisterClient(ctx context.Context, region string) (OIDCClient, error) {
	var resp struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	err := runAWSJSON(ctx, &resp, "sso-oidc", "register-client",
		"--client-name", "scriptstui", "--client-type", "public", "--region", region)
	if err != nil {
		return OIDCClient{}, err
	}
	return OIDCClient{ClientID: resp.ClientID, ClientSecret: resp.ClientSecret}, nil
}

// StartDeviceAuthorization kicks off the device-code flow for startURL:
// show the returned UserCode/VerificationURIComplete to the user (and try
// opening it in their browser), then call PollForToken.
func StartDeviceAuthorization(ctx context.Context, region string, client OIDCClient, startURL string) (DeviceAuthorization, error) {
	var resp struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		VerificationURI         string `json:"verificationUri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}
	err := runAWSJSON(ctx, &resp, "sso-oidc", "start-device-authorization",
		"--client-id", client.ClientID, "--client-secret", client.ClientSecret,
		"--start-url", startURL, "--region", region)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = 5
	}
	return DeviceAuthorization{
		DeviceCode:              resp.DeviceCode,
		UserCode:                resp.UserCode,
		VerificationURI:         resp.VerificationURI,
		VerificationURIComplete: resp.VerificationURIComplete,
		ExpiresIn:               resp.ExpiresIn,
		IntervalSeconds:         interval,
	}, nil
}

// OpenBrowser best-effort opens url in the default browser. Failures are
// harmless: the caller should always also show the URL as text.
func OpenBrowser(url string) {
	_ = exec.Command("open", url).Start()
}

// PollForToken blocks — polling at the pace the device authorization asked
// for — until the user approves the request in their browser, they deny
// it, the code expires, or ctx is cancelled.
func PollForToken(ctx context.Context, region string, client OIDCClient, device DeviceAuthorization) (string, error) {
	interval := time.Duration(device.IntervalSeconds) * time.Second
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return "", errors.New("el código expiró antes de que aprobaras en el navegador")
		}

		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "aws", "sso-oidc", "create-token",
			"--client-id", client.ClientID, "--client-secret", client.ClientSecret,
			"--grant-type", "urn:ietf:params:oauth:grant-type:device_code",
			"--device-code", device.DeviceCode, "--region", region, "--output", "json")
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			var resp struct {
				AccessToken string `json:"accessToken"`
			}
			if jerr := json.Unmarshal(out, &resp); jerr != nil {
				return "", jerr
			}
			return resp.AccessToken, nil
		}

		switch awsErrorName(stderr.String()) {
		case "AuthorizationPendingException":
			continue
		case "SlowDownException":
			interval += 5 * time.Second
			continue
		case "ExpiredTokenException":
			return "", errors.New("el código expiró antes de que aprobaras en el navegador")
		case "AccessDeniedException":
			return "", errors.New("se rechazó la autorización en el navegador")
		default:
			return "", errors.New(firstLine(stderr.String(), err))
		}
	}
}

// ListAccounts lists every AWS account the just-authorized user can see.
func ListAccounts(ctx context.Context, region, accessToken string) ([]Account, error) {
	var out []Account
	nextToken := ""
	for {
		args := []string{"sso", "list-accounts", "--access-token", accessToken, "--region", region}
		if nextToken != "" {
			args = append(args, "--starting-token", nextToken)
		}
		var resp struct {
			NextToken   string `json:"nextToken"`
			AccountList []struct {
				AccountID   string `json:"accountId"`
				AccountName string `json:"accountName"`
				Email       string `json:"emailAddress"`
			} `json:"accountList"`
		}
		if err := runAWSJSON(ctx, &resp, args...); err != nil {
			return nil, err
		}
		for _, a := range resp.AccountList {
			out = append(out, Account{AccountID: a.AccountID, AccountName: a.AccountName, Email: a.Email})
		}
		if resp.NextToken == "" {
			return out, nil
		}
		nextToken = resp.NextToken
	}
}

// ListAccountRoles lists every role the user can assume into accountID.
func ListAccountRoles(ctx context.Context, region, accessToken, accountID string) ([]Role, error) {
	var out []Role
	nextToken := ""
	for {
		args := []string{"sso", "list-account-roles", "--access-token", accessToken, "--account-id", accountID, "--region", region}
		if nextToken != "" {
			args = append(args, "--starting-token", nextToken)
		}
		var resp struct {
			NextToken string `json:"nextToken"`
			RoleList  []struct {
				RoleName  string `json:"roleName"`
				AccountID string `json:"accountId"`
			} `json:"roleList"`
		}
		if err := runAWSJSON(ctx, &resp, args...); err != nil {
			return nil, err
		}
		for _, r := range resp.RoleList {
			out = append(out, Role{RoleName: r.RoleName, AccountID: r.AccountID})
		}
		if resp.NextToken == "" {
			return out, nil
		}
		nextToken = resp.NextToken
	}
}

// ProfileName deterministically names the profile for an account+role pair.
func ProfileName(accountID, roleName string) string {
	return accountID + "-" + sanitizeName(roleName)
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// WriteProfile adds a [profile name] section to ~/.aws/config with the
// legacy flat SSO fields (no separate [sso-session] indirection needed —
// `aws sso login --profile name` works fine with this shape too). If a
// section with that name already exists, this is a no-op: profile names are
// derived from account+role, so an existing one just means it was already
// set up.
func WriteProfile(name, startURL, ssoRegion, accountID, roleName, cliRegion string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)

	header := "[profile " + name + "]"
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == header {
			return nil
		}
	}

	block := header + "\n" +
		"sso_start_url = " + startURL + "\n" +
		"sso_region = " + ssoRegion + "\n" +
		"sso_account_id = " + accountID + "\n" +
		"sso_role_name = " + roleName + "\n" +
		"region = " + cliRegion + "\n" +
		"output = json\n"

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" {
		content += "\n"
	}
	content += block

	return os.WriteFile(path, []byte(content), 0o600)
}

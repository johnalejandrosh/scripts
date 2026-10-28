package ssologin

import (
	"strings"
	"testing"
)

func TestNormalizeStartURL(t *testing.T) {
	for _, in := range []string{
		"https://d-90671b694f.awsapps.com/start",
		"https://d-90671b694f.awsapps.com/start/",
		"https://d-90671b694f.awsapps.com/start/#",
		"https://d-90671b694f.awsapps.com/start/#/",
		" https://d-90671b694f.awsapps.com/start# ",
	} {
		if got := NormalizeStartURL(in); got != "https://d-90671b694f.awsapps.com/start" {
			t.Errorf("NormalizeStartURL(%q) = %q", in, got)
		}
	}
}

// Three spellings of the same portal: the AWS CLI needs three logins.
const mixedConfig = `# mis cuentas
[default]
region = us-east-1

[profile a-legacy]
sso_start_url = https://d-90671b694f.awsapps.com/start
sso_region = us-east-1
sso_account_id = 111111111111
sso_role_name = Dev
region = us-east-1
output = json

[profile b-slash]
sso_start_url = https://d-90671b694f.awsapps.com/start/#
sso_region = us-east-1
sso_account_id = 222222222222
sso_role_name = Dev
region = us-east-1

[profile c-session]
sso_session = linktic
sso_account_id = 333333333333
sso_role_name = QA
region = us-west-2

[sso-session linktic]
sso_start_url = https://d-90671b694f.awsapps.com/start/
sso_region = us-east-1
sso_registration_scopes = sso:account:access

[profile plain]
aws_access_key_id = AKIAEXAMPLE
`

func TestLoginGroupsDetectsMixedConfig(t *testing.T) {
	groups, portals := LoginGroups(parseSSOProfiles(mixedConfig))
	if groups != 3 || portals != 1 {
		t.Fatalf("LoginGroups = %d grupos, %d portales; esperaba 3 y 1", groups, portals)
	}
}

func TestRewriteSSOConfigLeavesOneLogin(t *testing.T) {
	out, profiles, removed := rewriteSSOConfig(mixedConfig)

	if len(profiles) != 3 {
		t.Fatalf("reescribió %d perfiles, esperaba 3", len(profiles))
	}
	if len(removed) != 1 || removed[0] != "linktic" {
		t.Fatalf("sesiones eliminadas = %v, esperaba [linktic]", removed)
	}
	if groups, _ := LoginGroups(parseSSOProfiles(out)); groups != 1 {
		t.Fatalf("después de limpiar siguen %d logins distintos:\n%s", groups, out)
	}
	for _, gone := range []string{"sso_session", "[sso-session", "sso_registration_scopes", "start/"} {
		if strings.Contains(out, gone) {
			t.Errorf("la salida todavía contiene %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"# mis cuentas", "[default]", "region = us-west-2", "output = json", "aws_access_key_id = AKIAEXAMPLE"} {
		if !strings.Contains(out, kept) {
			t.Errorf("se perdió %q:\n%s", kept, out)
		}
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("quedaron líneas en blanco de más:\n%s", out)
	}
}

func TestRewriteSSOConfigKeepsSessionStillInUse(t *testing.T) {
	// A profile without account/role isn't rewritten, so its session stays.
	cfg := mixedConfig + "\n[profile only-session]\nsso_session = linktic\n"
	out, _, removed := rewriteSSOConfig(cfg)
	if len(removed) != 0 || !strings.Contains(out, "[sso-session linktic]") {
		t.Fatalf("eliminó una sesión que otro perfil sigue usando (removed=%v):\n%s", removed, out)
	}
}

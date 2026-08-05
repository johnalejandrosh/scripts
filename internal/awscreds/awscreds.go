// Package awscreds parses an AWS credentials block pasted by the user
// (either shell "export KEY=VALUE" lines or the ~/.aws/credentials ini
// format) and can validate it by calling `aws sts get-caller-identity`.
// Nothing is ever written to disk: credentials only ever live as env vars
// for the current process.
package awscreds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Env returns the AWS_* environment variable assignments for these
// credentials, suitable for appending to an exec.Cmd's Env.
func (c Credentials) Env() []string {
	env := []string{
		"AWS_ACCESS_KEY_ID=" + c.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + c.SecretAccessKey,
	}
	if c.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+c.SessionToken)
	}
	return env
}

// Parse extracts credentials from a pasted block of text. It accepts both
//
//	export AWS_ACCESS_KEY_ID=AKIA...
//	export AWS_SECRET_ACCESS_KEY=...
//	export AWS_SESSION_TOKEN=...
//
// and the ~/.aws/credentials ini style:
//
//	[default]
//	aws_access_key_id = AKIA...
//	aws_secret_access_key = ...
//	aws_session_token = ...
func Parse(text string) (Credentials, error) {
	var c Credentials
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)

		switch key {
		case "AWS_ACCESS_KEY_ID":
			c.AccessKeyID = val
		case "AWS_SECRET_ACCESS_KEY":
			c.SecretAccessKey = val
		case "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN":
			c.SessionToken = val
		}
	}

	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Credentials{}, errors.New("no encontré AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY en el texto pegado")
	}
	return c, nil
}

// Validate calls `aws sts get-caller-identity` using extraEnv (on top of the
// current process environment) and reports whether AWS accepted the
// credentials. A nil/empty extraEnv validates whatever credentials are
// already ambient (env vars, ~/.aws/credentials, etc).
func Validate(extraEnv []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "aws", "sts", "get-caller-identity")
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

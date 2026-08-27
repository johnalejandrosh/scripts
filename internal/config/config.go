package config

import (
	"fmt"
	"strings"
)

// StepKind distinguishes an AWS SSM tunnel step from a plain shell command step.
type StepKind int

const (
	StepTunnel StepKind = iota
	StepCommand
)

// Step is one process launched as part of a Service (tunnel, backend, frontend, ...).
type Step struct {
	Kind StepKind
	// Label is the prefix shown next to each log line, e.g. "AWS", "BACKEND".
	Label string
	// Dir is the working directory the command runs in. Empty means inherit.
	Dir string
	// Command + Args is the process to exec.
	Command string
	Args    []string
	// Raw is the command line exactly as written in this file, kept so it can
	// be shown/copied and pasted by hand into a terminal when needed.
	Raw string
	// WaitAfterStart mirrors the `sleep N; ps -p $PID` checks in the original
	// scripts: how long to wait before confirming the process didn't die
	// immediately. Zero means don't wait/check (used for single-step, foreground
	// SSM sessions where there is nothing to check against).
	WaitAfterStartSeconds int
	// Profile, when set, names an AWS CLI profile (see internal/ssologin)
	// that this tunnel step must run as: `aws ... --profile <Profile>`.
	// Left empty here — the TUI assigns it per session, from whatever SSO
	// profiles it discovers in the user's own ~/.aws/config.
	Profile string
}

// Service is the Go equivalent of one start-*.sh script: an ordered list of
// steps that are started in sequence and torn down together.
type Service struct {
	ID    string
	Title string
	Steps []Step
}

// tunnelStep builds a tunnel step from the full `aws ssm start-session ...`
// command line, written here exactly as it would be pasted into a terminal
// (line continuations and single-quoted --parameters JSON included).
func tunnelStep(label, cmdline string, waitSeconds int) Step {
	command, args := mustParseCmdline(cmdline)
	return Step{
		Kind:                  StepTunnel,
		Label:                 label,
		Command:               command,
		Args:                  args,
		Raw:                   normalizeCmdline(cmdline),
		WaitAfterStartSeconds: waitSeconds,
	}
}

func npmStep(label, dir string, args ...string) Step {
	return Step{
		Kind:                  StepCommand,
		Label:                 label,
		Dir:                   dir,
		Command:               "npm",
		Args:                  args,
		Raw:                   normalizeCmdline("npm " + strings.Join(args, " ")),
		WaitAfterStartSeconds: 2,
	}
}

// normalizeCmdline collapses a multi-line, backslash-continued command into a
// single line, for display and for copy/paste.
func normalizeCmdline(cmdline string) string {
	fields := strings.Fields(strings.ReplaceAll(cmdline, "\\\n", " "))
	return strings.Join(fields, " ")
}

// mustParseCmdline splits a shell-style command line into program + args.
// The command lines live in this file as literals, so a malformed one is a
// programming error and panics at startup rather than failing later.
func mustParseCmdline(cmdline string) (string, []string) {
	tokens, err := parseCmdline(cmdline)
	if err != nil {
		panic(fmt.Sprintf("config: comando inválido %q: %v", cmdline, err))
	}
	if len(tokens) == 0 {
		panic(fmt.Sprintf("config: comando vacío %q", cmdline))
	}
	return tokens[0], tokens[1:]
}

// parseCmdline tokenizes a command line the way a POSIX shell would for the
// subset used here: whitespace separates tokens, `\` at end of line continues
// it, and single/double quotes group text (the --parameters JSON relies on
// single quotes). No variable expansion, globbing or pipelines.
func parseCmdline(cmdline string) ([]string, error) {
	var (
		tokens  []string
		cur     strings.Builder
		started bool
	)
	flush := func() {
		if started {
			tokens = append(tokens, cur.String())
			cur.Reset()
			started = false
		}
	}

	runes := []rune(cmdline)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && i+1 < len(runes):
			// Line continuation: drop the backslash and the newline.
			if runes[i+1] == '\n' {
				i++
				continue
			}
			cur.WriteRune(runes[i+1])
			started = true
			i++
		case c == '\'' || c == '"':
			quote := c
			started = true
			i++
			for ; i < len(runes) && runes[i] != quote; i++ {
				// Inside double quotes a backslash still escapes; inside
				// single quotes everything is literal.
				if quote == '"' && runes[i] == '\\' && i+1 < len(runes) {
					i++
				}
				cur.WriteRune(runes[i])
			}
			if i == len(runes) {
				return nil, fmt.Errorf("comilla %q sin cerrar", string(quote))
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	flush()
	return tokens, nil
}

// CommandLine returns the step exactly as it will be run, ready to be copied
// and pasted into a terminal — including the `--profile` flag that procman
// appends when a profile is assigned. Steps built by this package carry their
// literal command line in Raw; anything else is reassembled with shell
// quoting so the pasted version still parses as one argument per arg.
func (s Step) CommandLine() string {
	line := s.Raw
	if line == "" {
		parts := make([]string, 0, len(s.Args)+1)
		parts = append(parts, shellQuote(s.Command))
		for _, a := range s.Args {
			parts = append(parts, shellQuote(a))
		}
		line = strings.Join(parts, " ")
	}
	if s.Profile != "" {
		line += " --profile " + shellQuote(s.Profile)
	}
	return line
}

// shellQuote wraps a token in single quotes when it holds anything the shell
// would otherwise interpret, escaping embedded single quotes the usual way.
func shellQuote(token string) string {
	if token == "" {
		return "''"
	}
	if strings.IndexFunc(token, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return token
	}
	return "'" + strings.ReplaceAll(token, "'", `'\''`) + "'"
}

// Services is the fixed list of environments this tool can start/stop.
// Only tunnels for now; appbi/map/simae (backend+frontend) are on hold.
// None of these hardcode an AWS profile — assign one per tunnel from the
// TUI's accounts panel ('a' to browse/login, 'p' on a tunnel to assign).
func Services() []Service {
	return []Service{
		{
			ID:    "db-balu",
			Title: "Túnel DB BALU PROD (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", `aws ssm start-session \
      --target i-074e88b2ee9d1d67f \
      --document-name AWS-StartPortForwardingSessionToRemoteHost \
      --parameters '{"host":["rds-gal-app-prod.cluster-cniu2mwmabwe.us-east-1.rds.amazonaws.com"],"portNumber":["5432"],"localPortNumber":["5437"]}' \
      --region us-east-1`, 0),
			},
		},
		{
			ID:    "db-balu-replica",
			Title: "Túnel DB BALU Read Replica (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", `aws ssm start-session \
      --target i-074e88b2ee9d1d67f \
      --document-name AWS-StartPortForwardingSessionToRemoteHost \
      --parameters '{"host":["read-replica.cniu2mwmabwe.us-east-1.rds.amazonaws.com"],"portNumber":["5432"],"localPortNumber":["5440"]}' \
      --region us-east-1`, 0),
			},
		},
		{
			ID:    "db-production",
			Title: "Túnel DB Producción BI CNE (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", `aws ssm start-session \
      --target i-02aa4717bbfd101b6 \
      --document-name AWS-StartPortForwardingSessionToRemoteHost \
      --parameters '{"host":["prod-testigos-v2-powerbi-svless.cluster-c4pyasay6pj9.us-east-1.rds.amazonaws.com"],"portNumber":["5432"],"localPortNumber":["5442"]}' \
      --region us-east-1`, 0),
			},
		},
		{
			ID:    "redis-balu",
			Title: "Túnel Redis BALU (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", `aws ssm start-session \
      --target i-074e88b2ee9d1d67f \
      --document-name AWS-StartPortForwardingSessionToRemoteHost \
      --parameters '{"host":["dev-balu-redis.tgplhj.0001.use1.cache.amazonaws.com"],"portNumber":["6379"],"localPortNumber":["6379"]}' \
      --region us-east-1`, 0),
			},
		},
		{
			ID:    "redshift-balu",
			Title: "Túnel Redshift BALU PROD (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", `aws ssm start-session \
      --target i-074e88b2ee9d1d67f \
      --document-name AWS-StartPortForwardingSessionToRemoteHost \
      --parameters '{"host":["balu-prod-workgroup.039612858373.us-east-1.redshift-serverless.amazonaws.com"],"portNumber":["5439"],"localPortNumber":["5439"]}' \
      --region us-east-1`, 0),
			},
		},
		{
			ID:    "fiduprevisora-qa",
			Title: "Túnel Fiduprevisora QA (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", `aws ssm start-session \
      --target i-0dfa5094b99b1d881 \
      --document-name AWS-StartPortForwardingSessionToRemoteHost \
      --parameters '{"host":["postgres-fiduprevisora-qa.c9u8ko0sale2.us-east-1.rds.amazonaws.com"],"portNumber":["5432"],"localPortNumber":["5434"]}' \
      --region us-east-1`, 0),
			},
		},
	}
}

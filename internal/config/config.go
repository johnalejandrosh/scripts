package config

import (
	"fmt"
	"strconv"
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
	// LocalPort is the port the tunnel listens on locally, kept as a value so
	// the UI and the store can read it without parsing the command line back.
	// Zero on command steps.
	LocalPort int
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
	raw := normalizeCmdline(cmdline)
	port, _ := strconv.Atoi(localPortNumber(raw))
	return Step{
		Kind:                  StepTunnel,
		Label:                 label,
		Command:               command,
		Args:                  args,
		Raw:                   raw,
		LocalPort:             port,
		WaitAfterStartSeconds: waitSeconds,
	}
}

// TunnelParams are the pieces of an SSM port-forwarding session that the
// tunnels table stores as columns. NewTunnelStep turns them into the same
// Step the literals above produce, so nothing downstream can tell whether a
// tunnel came from this file or from the database.
type TunnelParams struct {
	Label        string
	Target       string
	Host         string
	RemotePort   int
	LocalPort    int
	Region       string
	DocumentName string
	WaitSeconds  int
}

// SSMPortForwardDocument is the SSM document that forwards a local port to a
// host reachable from the target instance — the only one these tunnels use.
const SSMPortForwardDocument = "AWS-StartPortForwardingSessionToRemoteHost"

// NewTunnelStep builds a tunnel step from its parts instead of from a shell
// literal, so a row in the database becomes a runnable step without going
// through the command-line parser.
func NewTunnelStep(p TunnelParams) Step {
	label := p.Label
	if label == "" {
		label = "AWS"
	}
	doc := p.DocumentName
	if doc == "" {
		doc = SSMPortForwardDocument
	}
	parameters := fmt.Sprintf(
		`{"host":["%s"],"portNumber":["%d"],"localPortNumber":["%d"]}`,
		p.Host, p.RemotePort, p.LocalPort)

	args := []string{
		"ssm", "start-session",
		"--target", p.Target,
		"--document-name", doc,
		"--parameters", parameters,
		"--region", p.Region,
	}

	// Raw is the single-line, shell-quoted form, matching what
	// normalizeCmdline produces for the literals: it is what gets displayed
	// and copied, so the JSON has to stay quoted as one argument.
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "aws")
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}

	return Step{
		Kind:                  StepTunnel,
		Label:                 label,
		Command:               "aws",
		Args:                  args,
		Raw:                   strings.Join(parts, " "),
		LocalPort:             p.LocalPort,
		WaitAfterStartSeconds: p.WaitSeconds,
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

// Services is the seed for a brand-new database: the tunnels this tool
// shipped with before they moved into internal/store. It runs once, when
// scriptstui.db has no rows; from then on the tunnels live in the database
// and are managed from the UI ('n' new, 'e' edit, 'D' delete), so editing
// this list no longer changes what a populated install shows.
//
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

// LocalPort returns the local port this service's tunnel listens on, as text
// ready to print. Empty when the service has no tunnel step.
func (s Service) LocalPort() string {
	for _, step := range s.Steps {
		if step.Kind == StepTunnel && step.LocalPort > 0 {
			return strconv.Itoa(step.LocalPort)
		}
	}
	return ""
}

// localPortNumber pulls NNNN out of `"localPortNumber":["NNNN"]`.
func localPortNumber(cmdline string) string {
	return jsonArrayValue(cmdline, "localPortNumber")
}

// jsonArrayValue pulls V out of `"key":["V"]`. The --parameters payload is
// always written without spaces inside the JSON — both in the literals below
// and in NewTunnelStep — so a plain substring scan is enough, no JSON parsing.
func jsonArrayValue(s, key string) string {
	k := `"` + key + `":["`
	i := strings.Index(s, k)
	if i < 0 {
		return ""
	}
	rest := s[i+len(k):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TunnelParamsOf reads the port-forwarding parameters back out of a tunnel
// step's arguments. It is how the literals in this file get seeded into the
// database as columns; it reports false for anything that isn't a
// port-forwarding session.
func TunnelParamsOf(s Step) (TunnelParams, bool) {
	if s.Kind != StepTunnel {
		return TunnelParams{}, false
	}

	flags := make(map[string]string, 4)
	for i := 0; i+1 < len(s.Args); i++ {
		if strings.HasPrefix(s.Args[i], "--") {
			flags[s.Args[i]] = s.Args[i+1]
		}
	}

	params := flags["--parameters"]
	host := jsonArrayValue(params, "host")
	remote, errRemote := strconv.Atoi(jsonArrayValue(params, "portNumber"))
	local, errLocal := strconv.Atoi(jsonArrayValue(params, "localPortNumber"))
	if host == "" || errRemote != nil || errLocal != nil || flags["--target"] == "" {
		return TunnelParams{}, false
	}

	return TunnelParams{
		Label:        s.Label,
		Target:       flags["--target"],
		Host:         host,
		RemotePort:   remote,
		LocalPort:    local,
		Region:       flags["--region"],
		DocumentName: flags["--document-name"],
		WaitSeconds:  s.WaitAfterStartSeconds,
	}, true
}

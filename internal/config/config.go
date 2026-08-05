package config

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

func tunnelStep(label, target, region, params string, waitSeconds int) Step {
	return Step{
		Kind:    StepTunnel,
		Label:   label,
		Command: "aws",
		Args: []string{
			"ssm", "start-session",
			"--target", target,
			"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
			"--region", region,
			"--parameters", params,
		},
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
		WaitAfterStartSeconds: 2,
	}
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
				tunnelStep("AWS", "i-074e88b2ee9d1d67f", "us-east-1",
					`{"host":["rds-gal-app-prod.cluster-cniu2mwmabwe.us-east-1.rds.amazonaws.com"],"portNumber":["5432"],"localPortNumber":["5437"]}`, 0),
			},
		},
		{
			ID:    "db-production",
			Title: "Túnel DB Producción (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", "i-02aa4717bbfd101b6", "us-east-1",
					`{"host":["prod-testigos-v2-powerbi-svless.cluster-c4pyasay6pj9.us-east-1.rds.amazonaws.com"],"portNumber":["5432"],"localPortNumber":["5442"]}`, 0),
			},
		},
		{
			ID:    "redis-balu",
			Title: "Túnel Redis BALU (solo túnel)",
			Steps: []Step{
				tunnelStep("AWS", "i-074e88b2ee9d1d67f", "us-east-1",
					`{"host":["dev-balu-redis.tgplhj.0001.use1.cache.amazonaws.com"],"portNumber":["6379"],"localPortNumber":["6379"]}`, 0),
			},
		},
	}
}

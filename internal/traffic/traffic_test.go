package traffic

import "testing"

func TestParseListeners(t *testing.T) {
	got := parseListeners("p19269\nf20\nn127.0.0.1:5445\np19271\nf20\nn127.0.0.1:5433\nf21\nn[::1]:5433\n")
	if got[5445] != 19269 || got[5433] != 19271 || len(got) != 2 {
		t.Fatalf("parseListeners = %v", got)
	}
}

const nettopSample = `,bytes_in,bytes_out,
Google Chrome H.812,99,99,
tcp4 10.0.0.5:6000<->1.2.3.4:443,99,99,
session-manager.19269,20000,9000,
tcp4 127.0.0.1:5445<->*:*,,,
tcp4 127.0.0.1:5445<->127.0.0.1:61000,5000,2000,
tcp4 172.16.1.84:53665<->98.87.174.250:443,8522,6804,
tcp4 172.16.1.84:53662<->34.196.251.251:443,6303,1361,
session-manager.19271,8516,6816,
tcp4 127.0.0.1:5433<->*:*,,,
tcp4 172.16.1.84:53667<->98.87.174.250:443,8516,6816,
`

func TestParseNettopKeepsOnlyAWSConnections(t *testing.T) {
	flows := parseNettop(nettopSample)
	if len(flows[19269]) != 2 {
		t.Fatalf("pid 19269: %d conexiones, esperaba 2 (sin las locales): %v", len(flows[19269]), flows[19269])
	}
	var in, out uint64
	for _, f := range flows[19269] {
		in, out = in+f.in, out+f.out
	}
	if in != 8522+6303 || out != 6804+1361 {
		t.Fatalf("pid 19269 suma in=%d out=%d", in, out)
	}
	if len(flows[19271]) != 1 || flows[812] == nil {
		t.Fatalf("flows = %v", flows)
	}
}

func TestIsLocal(t *testing.T) {
	for conn, want := range map[string]bool{
		"tcp4 127.0.0.1:5445<->*:*":                  true,
		"tcp4 127.0.0.1:5445<->127.0.0.1:61000":      true,
		"tcp6 ::1.5445<->::1.61000":                  true,
		"tcp4 172.16.1.84:53665<->98.87.174.250:443": false,
	} {
		if got := isLocal(conn); got != want {
			t.Errorf("isLocal(%q) = %v, esperaba %v", conn, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[float64]string{
		0: "0B", 512: "512B", 1234: "1.2K", 34000: "34K", 1_200_000: "1.2M", 120_000_000: "120M", 3_400_000_000: "3.4G",
	} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%v) = %q, esperaba %q", n, got, want)
		}
	}
}

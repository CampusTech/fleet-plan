package diff

import (
	"strings"
	"testing"
)

const testMobileconfig = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>PayloadDisplayName</key>
    <string>Test Profile</string>
    <key>PayloadEnabled</key>
    <true/>
    <key>PayloadVersion</key>
    <integer>1</integer>
    <key>PayloadContent</key>
    <array>
      <dict>
        <key>PayloadType</key>
        <string>com.apple.wifi.managed</string>
        <key>Password</key>
        <string>hunter2</string>
        <key>Nested</key>
        <dict>
          <key>Deep</key>
          <string>value</string>
        </dict>
      </dict>
      <dict>
        <key>PayloadType</key>
        <string>com.apple.dock</string>
      </dict>
    </array>
  </dict>
</plist>
`

func TestPlistKeys(t *testing.T) {
	keys, err := profileKeys([]byte(testMobileconfig))
	if err != nil {
		t.Fatalf("profileKeys: %v", err)
	}

	want := map[string]string{
		"PayloadDisplayName":            "Test Profile",
		"PayloadEnabled":                "true",
		"PayloadVersion":                "1",
		"PayloadContent[0].PayloadType": "com.apple.wifi.managed",
		"PayloadContent[0].Password":    "hunter2",
		"PayloadContent[0].Nested.Deep": "value",
		"PayloadContent[1].PayloadType": "com.apple.dock",
	}
	for k, v := range want {
		if keys[k] != v {
			t.Errorf("%s: got %q, want %q", k, keys[k], v)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(keys), len(want), keys)
	}
}

func TestProfileKeysJSONDeclaration(t *testing.T) {
	const ddm = `{
		"Type": "com.apple.configuration.passcode.settings",
		"Identifier": "abc",
		"Payload": {"RequireAlphanumericPasscode": true, "MinimumLength": 8, "Codes": [1, 2]}
	}`

	keys, err := profileKeys([]byte(ddm))
	if err != nil {
		t.Fatalf("profileKeys: %v", err)
	}
	want := map[string]string{
		"Type":                                "com.apple.configuration.passcode.settings",
		"Identifier":                          "abc",
		"Payload.RequireAlphanumericPasscode": "true",
		"Payload.MinimumLength":               "8",
		"Payload.Codes[0]":                    "1",
		"Payload.Codes[1]":                    "2",
	}
	for k, v := range want {
		if keys[k] != v {
			t.Errorf("%s: got %q, want %q", k, keys[k], v)
		}
	}
}

func TestProfileKeysRejectsNonProfile(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"windows syncml", "<Replace><Item><Target><LocURI>./Device/Foo</LocURI></Target></Item></Replace>"},
		{"empty", ""},
		{"garbage", "not a profile at all"},
		{"malformed JSON", `{"unterminated": `},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := profileKeys([]byte(tt.content)); err == nil {
				t.Error("expected an error so the caller falls back to name-only matching")
			}
		})
	}
}

func TestProfileChecksum(t *testing.T) {
	// Fleet reports the base64 MD5 of the stored profile; this is the value
	// the diff compares against to skip downloading unchanged profiles.
	if got := profileChecksum([]byte("hello")); got != "XUFAKrxLKna5cZ2REBfFkg==" {
		t.Errorf("got %q, want XUFAKrxLKna5cZ2REBfFkg==", got)
	}
}

func TestProfileKeyChanges(t *testing.T) {
	tests := []struct {
		name     string
		current  map[string]string
		proposed map[string]string
		want     []string
	}{
		{
			name:     "identical",
			current:  map[string]string{"A": "1"},
			proposed: map[string]string{"A": "1"},
		},
		{
			name:     "changed value reports the key",
			current:  map[string]string{"A": "1"},
			proposed: map[string]string{"A": "2"},
			want:     []string{"A"},
		},
		{
			name:     "added key",
			current:  map[string]string{"A": "1"},
			proposed: map[string]string{"A": "1", "B": "2"},
			want:     []string{"+B"},
		},
		{
			name:     "removed key",
			current:  map[string]string{"A": "1", "B": "2"},
			proposed: map[string]string{"A": "1"},
			want:     []string{"-B"},
		},
		{
			// Fleet substitutes $VARS server-side, so the stored value never
			// equals the file's placeholder. Reporting it would mean every run
			// shows the same phantom change -- and the live value is a secret.
			name:     "env var placeholder is skipped",
			current:  map[string]string{"Secret": "actual-enroll-secret-value"},
			proposed: map[string]string{"Secret": "$FLEET_GLOBAL_ENROLL_SECRET"},
		},
		{
			name:     "sorted output",
			current:  map[string]string{"B": "1", "A": "1"},
			proposed: map[string]string{"B": "2", "A": "2"},
			want:     []string{"A", "B"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := profileKeyChanges(tt.current, tt.proposed)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProfileDiffSummary(t *testing.T) {
	tests := []struct {
		name    string
		changed []string
		want    string
	}{
		{name: "none", changed: nil, want: ""},
		{name: "one", changed: []string{"A"}, want: "1 key changed: A"},
		{name: "several", changed: []string{"A", "+B", "-C"}, want: "3 keys changed: A, +B, -C"},
		{
			name:    "truncated",
			changed: []string{"A", "B", "C", "D", "E", "F", "G"},
			want:    "7 keys changed: A, B, C, D, E, +2 more",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := profileDiffSummary(tt.changed); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// Values must never appear in the rendered summary: profiles carry
// certificates, passwords, and enroll secrets, and this text is posted to MRs.
func TestProfileDiffSummaryNeverIncludesValues(t *testing.T) {
	current := map[string]string{"PayloadContent[0].Password": "super-secret-value"}
	proposed := map[string]string{"PayloadContent[0].Password": "another-secret"}

	summary := profileDiffSummary(profileKeyChanges(current, proposed))
	for _, secret := range []string{"super-secret-value", "another-secret"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("summary leaked a value: %q", summary)
		}
	}
	if summary != "1 key changed: PayloadContent[0].Password" {
		t.Errorf("summary: got %q", summary)
	}
}

func TestPlistKeysMalformed(t *testing.T) {
	// Each of these must return an error so diffProfiles falls back to
	// name-only matching instead of reporting nonsense keys.
	tests := []struct {
		name    string
		content string
	}{
		{"truncated document", `<plist><dict><key>a</key><string>b`},
		{"truncated inside a key", `<plist><dict><key>abc`},
		{"truncated inside a value", `<plist><dict><key>a</key><string>abc`},
		{"stray angle bracket in a value", `<plist><dict><key>a</key><string><</string></dict></plist>`},
		// Malformed markup outside any element the walker decodes directly,
		// so the failure surfaces from the token loop rather than a decode.
		{"stray angle bracket in an array", `<plist><dict><key>a</key><array><</array></dict></plist>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := plistKeys([]byte(tt.content)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestPlistKeysValueOutsideContainer(t *testing.T) {
	// A plist whose root is a bare scalar rather than a dict: there is no
	// container to name the value, so it lands under the empty path.
	keys, err := plistKeys([]byte(`<plist version="1.0"><string>bare</string></plist>`))
	if err != nil {
		t.Fatalf("plistKeys: %v", err)
	}
	if keys[""] != "bare" {
		t.Errorf("got %v, want the value under the empty path", keys)
	}
}

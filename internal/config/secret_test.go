package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/sethvargo/go-envconfig"
)

// leakCanary is the plaintext every redaction test looks for. Finding it in
// output is the failure; the tests assert its absence as well as the
// placeholder's presence, because a formatter that silently emits nothing
// would satisfy the second check alone.
const leakCanary = "hunter2"

// TestSecretStringRedactsInEveryStringRepresentation covers AC-001.26.
func TestSecretStringRedactsInEveryStringRepresentation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format func(SecretString) string
	}{
		{
			name:   "String",
			format: func(s SecretString) string { return s.String() },
		},
		{
			name: "fmt %s",
			// Calling String directly, as gocritic suggests, would test the
			// method rather than the routing: the point is that fmt finds it.
			//nolint:gocritic,staticcheck // see comment above
			format: func(s SecretString) string { return fmt.Sprintf("%s", s) },
		},
		{
			name: "fmt %v",
			//nolint:gocritic // see the %s case above
			format: func(s SecretString) string { return fmt.Sprintf("%v", s) },
		},
		{
			name:   "fmt %q",
			format: func(s SecretString) string { return fmt.Sprintf("%q", s) },
		},
		{
			name:   "LogValue",
			format: func(s SecretString) string { return s.LogValue().String() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.format(SecretString(leakCanary))

			if strings.Contains(got, leakCanary) {
				t.Errorf("formatted secret = %q, want the plaintext absent", got)
			}

			if !strings.Contains(got, redacted) {
				t.Errorf("formatted secret = %q, want it to contain %q", got, redacted)
			}
		})
	}
}

// TestSecretStringRedactsWhenItHoldsNothing covers AC-001.26.
func TestSecretStringRedactsWhenItHoldsNothing(t *testing.T) {
	t.Parallel()

	// An empty secret redacts too. Passing the value through unchanged when it
	// happens to be empty would make the output depend on the secret's
	// contents, which is a side channel however small.
	if got := SecretString("").String(); got != redacted {
		t.Errorf("SecretString(\"\").String() = %q, want %q", got, redacted)
	}
}

// TestSecretStringLogValueIsAString covers AC-001.26.
func TestSecretStringLogValueIsAString(t *testing.T) {
	t.Parallel()

	// slog does not consult fmt.Stringer for a string-kinded value, so the
	// LogValuer implementation is what stands between a secret and the log.
	if got := SecretString(leakCanary).LogValue().Kind(); got != slog.KindString {
		t.Errorf("LogValue().Kind() = %v, want %v", got, slog.KindString)
	}
}

// TestSecretStringDoesNotReachAnSlogHandler covers AC-001.26.
func TestSecretStringDoesNotReachAnSlogHandler(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("msg", "password", SecretString(leakCanary))

	if strings.Contains(buf.String(), leakCanary) {
		t.Errorf("log output = %s, want the plaintext absent", buf.String())
	}

	if !strings.Contains(buf.String(), redacted) {
		t.Errorf("log output = %s, want it to contain %q", buf.String(), redacted)
	}
}

// TestSecretStringExposeReturnsThePlaintext covers AC-001.27.
func TestSecretStringExposeReturnsThePlaintext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    SecretString
		want string
	}{
		{name: "a secret", s: SecretString(leakCanary), want: leakCanary},
		{name: "no secret", s: SecretString(""), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.s.Expose(); got != tt.want {
				t.Errorf("Expose() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSecretStringMarshalsAsRedactedText covers AC-001.26.
func TestSecretStringMarshalsAsRedactedText(t *testing.T) {
	t.Parallel()

	got, err := SecretString(leakCanary).MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v, want nil", err)
	}

	if string(got) != redacted {
		t.Errorf("MarshalText() = %q, want %q", got, redacted)
	}
}

// TestSecretStringUnmarshalsTextVerbatim covers AC-001.28.
func TestSecretStringUnmarshalsTextVerbatim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text []byte
		want string
	}{
		{name: "a value", text: []byte(leakCanary), want: leakCanary},
		{name: "an empty value", text: []byte(""), want: ""},
		{name: "no value at all", text: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Decoding keeps the plaintext: redaction applies on the way out,
			// not on the way in, or the loaded secret would be unusable.
			var got SecretString

			if err := got.UnmarshalText(tt.text); err != nil {
				t.Fatalf("UnmarshalText() error = %v, want nil", err)
			}

			if got.Expose() != tt.want {
				t.Errorf("Expose() after UnmarshalText = %q, want %q", got.Expose(), tt.want)
			}
		})
	}
}

// TestSecretStringMarshalsAsRedactedJSON covers AC-001.26.
func TestSecretStringMarshalsAsRedactedJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Public string       `json:"public"`
		Hidden SecretString `json:"hidden"`
	}

	secret := SecretString(leakCanary)

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "on its own", value: secret, want: `"` + redacted + `"`},
		// Marshalling through a pointer must redact as well, or taking the
		// address of a secret anywhere along the way would defeat it.
		{name: "through a pointer", value: &secret, want: `"` + redacted + `"`},
		{
			name:  "as a struct field beside a public one",
			value: payload{Public: "ok", Hidden: secret},
			want:  `{"public":"ok","hidden":"` + redacted + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v, want nil", err)
			}

			if string(got) != tt.want {
				t.Errorf("json.Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestSecretStringUnmarshalsJSONStrings covers AC-001.28.
func TestSecretStringUnmarshalsJSONStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "a string", data: `"` + leakCanary + `"`, want: leakCanary},
		{name: "an empty string", data: `""`, want: ""},
		{name: "null", data: jsonNull, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Seeded with a prior value so that null is shown to clear the
			// field rather than merely leaving an already-empty one alone.
			got := SecretString("prior")

			if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
				t.Fatalf("json.Unmarshal() error = %v, want nil", err)
			}

			if got.Expose() != tt.want {
				t.Errorf("Expose() after json.Unmarshal = %q, want %q", got.Expose(), tt.want)
			}
		})
	}
}

// TestSecretStringUnmarshalsJSONIntoAStructField covers AC-001.28.
func TestSecretStringUnmarshalsJSONIntoAStructField(t *testing.T) {
	t.Parallel()

	type envelope struct {
		Public string       `json:"public"`
		Token  SecretString `json:"token"`
	}

	var got envelope

	if err := json.Unmarshal([]byte(`{"public":"ok","token":"`+leakCanary+`"}`), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", err)
	}

	want := envelope{Public: "ok", Token: SecretString(leakCanary)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("decoded envelope mismatch (-want +got):\n%s", diff)
	}

	// Still redacts after decoding: the plaintext arrived, the representation
	// did not change with it.
	if got.Token.String() != redacted {
		t.Errorf("String() after decoding = %q, want %q", got.Token.String(), redacted)
	}
}

// TestSecretStringRejectsJSONThatIsNotAString covers AC-001.28.
func TestSecretStringRejectsJSONThatIsNotAString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "a number", data: `42`},
		{name: "a boolean", data: `true`},
		{name: "an object", data: `{"nested":true}`},
		{name: "an array", data: `["a"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var s SecretString

			err := json.Unmarshal([]byte(tt.data), &s)
			if err == nil {
				t.Fatalf("json.Unmarshal(%s) error = nil, want a type failure", tt.data)
			}

			if !strings.Contains(err.Error(), "secret string") {
				t.Errorf("error = %q, want it to name the secret string", err)
			}

			if s.Expose() != "" {
				t.Errorf("Expose() = %q, want the secret left unset after a failure", s.Expose())
			}
		})
	}
}

// TestSecretStringWrapsMalformedJSONErrors covers AC-001.28.
func TestSecretStringWrapsMalformedJSONErrors(t *testing.T) {
	t.Parallel()

	// UnmarshalJSON is called directly: json.Unmarshal rejects a truncated
	// escape in its own scanner before any custom unmarshaler runs, so going
	// through it would not exercise this wrapping at all.
	var s SecretString

	err := s.UnmarshalJSON([]byte(`"\u`))
	if err == nil {
		t.Fatal("UnmarshalJSON() error = nil, want a syntax failure")
	}

	if !strings.Contains(err.Error(), "secret string") {
		t.Errorf("error = %q, want it to name the secret string", err)
	}
}

// TestSecretStringSurvivesAJSONRoundTripAsRedactedText covers AC-001.26.
func TestSecretStringSurvivesAJSONRoundTripAsRedactedText(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(SecretString(leakCanary))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v, want nil", err)
	}

	var decoded SecretString

	if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", unmarshalErr)
	}

	// The secret does not survive the round trip, by design: marshalling emits
	// the placeholder, so re-reading that output yields the placeholder. A
	// marshalled config is a report, never a source to reload from.
	if decoded.Expose() != redacted {
		t.Errorf("Expose() after a round trip = %q, want %q", decoded.Expose(), redacted)
	}
}

// TestDecodeLoadsSecretStringFieldsWithoutRevealingThem cover AC-001.26 and AC-001.28.
func TestDecodeLoadsSecretStringFieldsWithoutRevealingThem(t *testing.T) {
	t.Parallel()

	type withSecret struct {
		Token SecretString `env:"API_TOKEN" validate:"required"`
		Inner struct {
			Key SecretString `env:"KEY" validate:"required"`
		} `env:",prefix=INNER_"`
		Optional SecretString `env:"OPTIONAL"`
	}

	var got withSecret

	err := Decode(t.Context(), &got, envconfig.MapLookuper(map[string]string{
		"API_TOKEN": leakCanary,
		"INNER_KEY": "nested-" + leakCanary,
	}))
	if err != nil {
		t.Fatalf("Decode() error = %v, want nil", err)
	}

	if got.Token.Expose() != leakCanary {
		t.Errorf("Token.Expose() = %q, want %q", got.Token.Expose(), leakCanary)
	}

	if got.Inner.Key.Expose() != "nested-"+leakCanary {
		t.Errorf("Inner.Key.Expose() = %q, want the prefixed value", got.Inner.Key.Expose())
	}

	if got.Optional.Expose() != "" {
		t.Errorf("Optional.Expose() = %q, want it left unset", got.Optional.Expose())
	}

	// Printing the whole struct is what a hurried debugging session does, so
	// that is the case worth pinning.
	if dump := fmt.Sprintf("%v", got); strings.Contains(dump, leakCanary) {
		t.Errorf("formatted config = %s, want no plaintext secret", dump)
	}
}

// TestDecodeValidatesSecretStringFields covers AC-001.28.
func TestDecodeValidatesSecretStringFields(t *testing.T) {
	t.Parallel()

	type withSecret struct {
		Token SecretString `env:"API_TOKEN" validate:"required"`
	}

	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "absent", env: map[string]string{}},
		{name: "present but empty", env: map[string]string{"API_TOKEN": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg withSecret

			err := Decode(t.Context(), &cfg, envconfig.MapLookuper(tt.env))
			if err == nil {
				t.Fatal("Decode() error = nil, want a validation failure")
			}

			if !strings.Contains(err.Error(), "validating configuration") {
				t.Errorf("Decode() error = %q, want it to name the validation stage", err)
			}
		})
	}
}

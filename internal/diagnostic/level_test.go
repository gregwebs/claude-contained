package diagnostic

import "testing"

func TestResolveLevelUsesDeclaredPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		flagSet bool
		env     string
		logOnly bool
		want    Resolution
		wantErr bool
	}{
		{"default off", "", false, "", false, Resolution{LevelOff, SourceDefault}, false},
		{"log only implies info", "", false, "", true, Resolution{LevelInfo, SourceLogOnly}, false},
		{"environment", "", false, "debug", true, Resolution{LevelDebug, SourceEnvironment}, false},
		{"flag", "warn", true, "debug", true, Resolution{LevelWarn, SourceFlag}, false},
		{"explicit off prevents implication", "off", true, "debug", true, Resolution{LevelOff, SourceFlag}, false},
		{"valid flag rescues invalid environment", "error", true, "LOUD", false, Resolution{LevelError, SourceFlag}, false},
		{"invalid selected flag", "LOUD", true, "info", false, Resolution{}, true},
		{"invalid selected environment", "", false, "LOUD", false, Resolution{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveLevel(tt.flag, tt.flagSet, tt.env, tt.logOnly)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveLevel error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ResolveLevel = %#v, want %#v", got, tt.want)
			}
		})
	}
}

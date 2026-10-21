package validators

import "testing"

func TestHotkeyConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     HotkeyConfig
		wantErr bool
	}{
		{"valid", HotkeyConfig{Modifiers: 2, VK: 65, Label: "Ctrl+A"}, false},
		{"empty label", HotkeyConfig{Modifiers: 2, VK: 65, Label: ""}, true},
		{"modifiers below range", HotkeyConfig{Modifiers: -1, VK: 65, Label: "x"}, true},
		{"modifiers above range", HotkeyConfig{Modifiers: 16, VK: 65, Label: "x"}, true},
		{"vk below range", HotkeyConfig{Modifiers: 2, VK: -1, Label: "x"}, true},
		{"vk above range", HotkeyConfig{Modifiers: 2, VK: 256, Label: "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHotkeyConfig_Validate_Nil(t *testing.T) {
	var c *HotkeyConfig
	if err := c.Validate(); err == nil {
		t.Error("nil *HotkeyConfig.Validate() 应返回错误")
	}
}

func TestParseTimeOfDay(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		h, m, s int
	}{
		{"HH:MM valid", "08:30", false, 8, 30, 0},
		{"HH:MM:SS valid", "23:59:59", false, 23, 59, 59},
		{"empty", "", true, 0, 0, 0},
		{"single part", "12", true, 0, 0, 0},
		{"four parts", "1:2:3:4", true, 0, 0, 0},
		{"hour out of range", "24:00", true, 0, 0, 0},
		{"minute out of range", "12:60", true, 0, 0, 0},
		{"second out of range", "12:00:60", true, 0, 0, 0},
		{"non-numeric", "ab:cd", true, 0, 0, 0},
		{"with surrounding spaces", " 09:05 ", false, 9, 5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTimeOfDay(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTimeOfDay() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if got.Hour != tt.h || got.Minute != tt.m || got.Second != tt.s || !got.Valid {
				t.Errorf("ParseTimeOfDay() = %+v, want {H:%d M:%d S:%d Valid:true}", got, tt.h, tt.m, tt.s)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"windows absolute", `C:\foo\bar`, false},
		{"unix absolute", "/usr/local/bin", false},
		{"relative", "foo/bar", true},
		{"windows traversal", `C:\foo\..\bar`, true},
		{"unix traversal", "/foo/../bar", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePath(tt.path); (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		nm      string
		min     int
		max     int
		wantErr bool
	}{
		{"valid", "My Tool", 2, 20, false},
		{"empty", "", 1, 20, true},
		{"too short", "a", 2, 20, true},
		{"too long", "abcdefghijklmnopqrstuvwxyz", 1, 5, true},
		{"illegal colon", "a:b", 1, 20, true},
		{"illegal lt", "a<b", 1, 20, true},
		{"boundary min exactly", "ab", 2, 20, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateName(tt.nm, tt.min, tt.max); (err != nil) != tt.wantErr {
				t.Errorf("ValidateName(%q,%d,%d) error = %v, wantErr %v", tt.nm, tt.min, tt.max, err, tt.wantErr)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"http valid", "http://example.com", false},
		{"https valid with path and query", "https://example.com/path?q=1#frag", false},
		{"empty", "", true},
		{"no scheme", "example.com", true},
		{"ftp scheme", "ftp://example.com", true},
		{"space invalid", "http://exa mple.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

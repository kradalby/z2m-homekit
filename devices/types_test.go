package devices

import "testing"

func TestZ2MBrightnessToHAP(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"min value", 0, 0},
		{"max value", 254, 100},
		{"mid value", 127, 50},
		{"quarter value", 64, 25},   // 64 * 100 / 254 = 25.2 -> 25
		{"three quarters", 191, 75}, // 191 * 100 / 254 = 75.2 -> 75
		{"above max", 300, 100},
		{"negative", -10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Z2MBrightnessToHAP(tt.input)
			if got != tt.want {
				t.Errorf("Z2MBrightnessToHAP(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestHAPBrightnessToZ2M(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"min value", 0, 0},
		{"max value", 100, 254},
		{"mid value", 50, 127},
		{"quarter value", 25, 63},   // int(25 * 254 / 100) = int(63.5) = 63
		{"three quarters", 75, 190}, // int(75 * 254 / 100) = int(190.5) = 190
		{"above max", 150, 254},
		{"negative", -10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HAPBrightnessToZ2M(tt.input)
			if got != tt.want {
				t.Errorf("HAPBrightnessToZ2M(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestBrightnessRoundTrip(t *testing.T) {
	// Test that converting from HAP to Z2M and back gives reasonable results
	for hap := 0; hap <= 100; hap++ {
		z2m := HAPBrightnessToZ2M(hap)
		back := Z2MBrightnessToHAP(z2m)
		// Allow +/- 1 difference due to rounding
		if abs(back-hap) > 1 {
			t.Errorf("Round trip failed: HAP %d -> Z2M %d -> HAP %d (diff: %d)", hap, z2m, back, back-hap)
		}
	}
}

func TestClampColorTemp(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"within range", 250, 250},
		{"at min", 140, 140},
		{"at max", 500, 500},
		{"below min", 100, 140},
		{"above max", 600, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClampColorTemp(tt.input)
			if got != tt.want {
				t.Errorf("ClampColorTemp(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestPtr(t *testing.T) {
	// Test int pointer
	intVal := 42
	intPtr := Ptr(intVal)
	if *intPtr != intVal {
		t.Errorf("Ptr(%d) = %d, want %d", intVal, *intPtr, intVal)
	}

	// Test string pointer
	strVal := "hello"
	strPtr := Ptr(strVal)
	if *strPtr != strVal {
		t.Errorf("Ptr(%q) = %q, want %q", strVal, *strPtr, strVal)
	}

	// Test bool pointer
	boolVal := true
	boolPtr := Ptr(boolVal)
	if *boolPtr != boolVal {
		t.Errorf("Ptr(%v) = %v, want %v", boolVal, *boolPtr, boolVal)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

package scan

import "testing"

func TestIsDotenv(t *testing.T) {
	for name, want := range map[string]bool{
		".env": true, ".env.example": true, "app/.env": true, "stage.env": true, "deploy/prod.env.example": true,
		"env.php": false, "config/env.go": false, ".envrc": false, "README.env.md": false, "environment.yaml": false,
	} {
		if got := IsDotenv(name); got != want {
			t.Errorf("IsDotenv(%q) = %v, want %v", name, got, want)
		}
	}
}

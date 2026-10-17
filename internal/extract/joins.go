package extract

import (
	"path"
	"strings"
)

// Repositories join through two kinds of names without any configuration: environment variable names and image
// repositories. Both are joined only when they are globally unique. PORT, DEBUG or APP_NAME mean the same thing in every
// project and say nothing about a link between two of them, and postgres or nginx name a public image, not a service that
// another repository deploys. So the lists below leave those out, and an image needs an owner ("org/app").

// genericEnvPrefixes are the prefixes of variables that every framework or service reads.
var genericEnvPrefixes = []string{
	"APP_", "DB_", "REDIS_", "LOG_", "CACHE_", "SESSION_", "MAIL_", "QUEUE_", "AWS_", "BROADCAST_", "FILESYSTEM_", "VITE_", "MIX_",
}

// genericEnvNames are variables that are common to all projects, whatever their prefix.
var genericEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "SHELL": true, "LANG": true, "TZ": true, "PWD": true, "HOSTNAME": true,
	"PORT": true, "HOST": true, "NODE_ENV": true, "DEBUG": true, "CI": true, "TERM": true, "GOPATH": true, "GOOS": true,
	"GOARCH": true,
}

// joinableEnv says whether an environment variable name can join repositories: at least four characters, an underscore, only
// upper-case letters, digits and underscores, and not generic.
func joinableEnv(name string) bool {
	if len(name) < 4 || !strings.Contains(name, "_") {
		return false
	}
	for _, c := range name {
		if c != '_' && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	for _, p := range genericEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return !genericEnvNames[name]
}

// fileExtensions are the endings of a value that names a file, not an image: `image: assets/logo.png` in a template.
var fileExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true, ".ico": true,
	".yaml": true, ".yml": true, ".json": true, ".txt": true, ".md": true,
}

// imageRepository normalises an image reference to the repository it names: the registry host, the tag and the digest
// are dropped, so ghcr.io/org/app:1.2 and registry.example.com/org/app@sha256:... are both org/app. It returns "" for a
// reference that cannot join: a template or a variable, an image without an owner, or a file name.
func imageRepository(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" || strings.ContainsAny(ref, " \t{}$\"'`") {
		return ""
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	last := len(parts) - 1
	if i := strings.Index(parts[last], ":"); i >= 0 {
		parts[last] = parts[last][:i]
	}
	if len(parts) < 2 || parts[0] == "" || parts[last] == "" || fileExtensions[path.Ext(parts[last])] {
		return ""
	}
	return strings.Join(parts, "/")
}

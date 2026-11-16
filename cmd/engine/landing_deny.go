package main

import (
	"path"
	"strings"
)

// deniedLandingNames are file and directory names never served from a landing.
// They hold logs, secrets, package manifests or server configuration.
var deniedLandingNames = map[string]struct{}{
	"log.json":          {},
	"logs.json":         {},
	"error_log":         {},
	"groups.json":       {},
	"s2s.json":          {},
	"composer.json":     {},
	"composer.lock":     {},
	"package.json":      {},
	"package-lock.json": {},
	"vendor":            {},
	"node_modules":      {},
	"log":               {},
	"logs":              {},
	"web.config":        {},
}

// deniedLandingExts are extensions never served as static landing files.
// PHP sources other than .php, backups, dumps, archives and keys are here;
// .php itself is only executed by the PHP sidecar.
var deniedLandingExts = map[string]struct{}{
	".log": {}, ".phps": {}, ".inc": {}, ".phtml": {}, ".phar": {},
	".php3": {}, ".php4": {}, ".php5": {}, ".php7": {}, ".php8": {},
	".sql": {}, ".sqlite": {}, ".db": {}, ".bak": {}, ".old": {}, ".orig": {},
	".swp": {}, ".swo": {}, ".tmp": {}, ".save": {}, ".lock": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {}, ".rar": {}, ".7z": {},
	".env": {}, ".ini": {}, ".pem": {}, ".key": {}, ".sh": {},
}

// allowedLandingExts is the set of file extensions that may be served as static
// landing assets. Any extension not in this list (and not .php, which is proxied
// to the PHP sidecar, or the empty extension, which triggers a redirect) results
// in a 404 from serveAsset. Matching is done on a lower-case extension.
var allowedLandingExts = map[string]struct{}{
	".html": {}, ".htm": {}, ".css": {}, ".js": {}, ".mjs": {}, ".map": {},
	".json": {}, ".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {},
	".avif": {}, ".svg": {}, ".ico": {}, ".bmp": {}, ".woff": {}, ".woff2": {},
	".ttf": {}, ".otf": {}, ".eot": {}, ".mp4": {}, ".webm": {}, ".ogg": {},
	".mp3": {}, ".wav": {}, ".pdf": {}, ".webmanifest": {},
}

// landingDenied reports whether rel names a service or sensitive landing file.
// rel is the cleaned relative path inside one landing. Every segment is checked,
// so a denied directory hides everything below it. Matching ignores case.
// Denied are dot names, names from deniedLandingNames, extensions from
// deniedLandingExts, editor backups ending in '~' and JSON logs such as
// error-log.json. The result is true when the file must answer 404.
func landingDenied(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if deniedLandingSegment(strings.ToLower(seg)) {
			return true
		}
	}
	return false
}

// deniedLandingSegment applies the deny rules to one lower-case path segment.
// seg is a file or directory name. The result is true for a denied name.
func deniedLandingSegment(seg string) bool {
	if seg == "" || strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, "~") {
		return true
	}
	if _, ok := deniedLandingNames[seg]; ok {
		return true
	}
	ext := path.Ext(seg)
	if _, ok := deniedLandingExts[ext]; ok {
		return true
	}
	return ext == ".json" && jsonLogName(strings.TrimSuffix(seg, ext))
}

// jsonLogName reports whether stem, a JSON file name without .json, names a log,
// a data export, sensitive configuration, or secrets. The stem is split on any
// character that is not a letter or a digit; a matching part marks the name.
// blog.json and data.json stay allowed.
func jsonLogName(stem string) bool {
	parts := strings.FieldsFunc(stem, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, p := range parts {
		switch p {
		case "log", "logs", "error", "errors",
			"lead", "leads", "order", "orders",
			"config", "settings", "users", "customers", "contacts",
			"backup", "dump", "secret", "secrets",
			"credentials", "token", "tokens":
			return true
		}
	}
	return false
}

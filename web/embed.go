package web

import "embed"

// Static holds the embedded landing page + css/js/icons under web/static.
//
// Directory layout:
//
//	web/static/index.html
//	web/static/css/style.css
//	web/static/js/app.js
//	web/static/icons/*.svg (Lucide icons, see icons/ICONS.md)
//
// Served by internal/api/server.go at:
//
//	GET /          -> static/index.html (text/html)
//	GET /static/*  -> files below, with correct Content-Type + caching
//
//go:embed static
var Static embed.FS

package web

import "embed"

// Assets contains the landing page and administrator interface shipped with the service.
//
//go:embed templates/*.html static/*
var Assets embed.FS

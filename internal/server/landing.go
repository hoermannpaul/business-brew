package server

import (
	"net/http"

	"business-brew/web"
)

func (s *Server) landingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The public page uses only embedded scripts and images. Administrator pages
	// retain the stricter, script-free policy applied by the shared handler.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	_ = s.templates.ExecuteTemplate(w, "landing", web.LandingData{
		BasePath: "/", ManagementURL: "/admin", ManagementLabel: "Open the admin console",
		NavLabel: "Admin sign in", FooterLabel: "Admin console",
	})
}

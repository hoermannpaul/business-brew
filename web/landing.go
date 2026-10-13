package web

// LandingData keeps the service and static site on the same landing template.
type LandingData struct {
	BasePath        string
	ManagementURL   string
	ManagementLabel string
	NavLabel        string
	FooterLabel     string
}

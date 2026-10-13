// Command business-brew-site exports the public landing page for static hosting.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"business-brew/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("business-brew-site", flag.ContinueOnError)
	output := flags.String("output", "dist", "Output directory")
	basePath := flags.String("base-path", "/", "Hosting path, such as /business-brew/")
	repositoryURL := flags.String("repository-url", "https://github.com/hoermannpaul/business-brew", "Public repository URL for setup documentation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*output) == "" {
		return errors.New("provide a nonempty output directory and no positional arguments")
	}
	if !strings.HasPrefix(*basePath, "/") || strings.HasPrefix(*basePath, "//") || strings.ContainsAny(*basePath, "?#\\") {
		return errors.New("base-path must be an absolute URL path")
	}
	*basePath = path.Clean(*basePath)
	if *basePath != "/" {
		*basePath += "/"
	}
	repo, err := url.Parse(*repositoryURL)
	if err != nil || repo.Scheme != "https" || repo.Host == "" || repo.User != nil || repo.RawQuery != "" || repo.Fragment != "" {
		return errors.New("repository-url must be a plain HTTPS URL")
	}
	t, err := template.ParseFS(web.Assets, "templates/landing.html")
	if err != nil {
		return err
	}
	var page bytes.Buffer
	if err := t.ExecuteTemplate(&page, "landing", web.LandingData{
		BasePath: *basePath, ManagementURL: strings.TrimRight(repo.String(), "/") + "#self-hosting",
		ManagementLabel: "Read the self-hosting guide", NavLabel: "Self-host Business Brew", FooterLabel: "Source & setup",
	}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*output, "static"), 0755); err != nil {
		return err
	}
	// Export only the public assets. Administrator pages and runtime data never
	// enter the GitHub Pages artifact.
	for _, name := range []string{"landing.css", "landing.js", "brew-mark.svg"} {
		asset, err := web.Assets.ReadFile("static/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*output, "static", name), asset, 0644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(*output, "index.html"), page.Bytes(), 0644)
}

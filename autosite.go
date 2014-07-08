// Package autosite defines Google App Engine sites automatically
// based on file structure.
//
// Example usage:
//   mysite := New(
//     "Some title",   // for HTML <head>
//     "pages/*.tmpl", // pattern for pages on disk
//     "domain.com",   // live domain
//     []string{       // shared templates
//       "base.tmpl",
//       "other.tmpl",
//     },
//   )
//   mysite.Register()
//
// This will host pages like domain.com/Foo and /Bar if there's
// files pages/Foo.tmpl and pages/Bar.tmpl relative to the calling
// package, also using "base.tmpl" and "other.tmpl" to compile the
// templates for rendering those pages.
//
// The following data is available within each template:
//   {{.Title}}: The <title> of the page.
//   {{.Date.Year}}, {{.Date.Month}}: Year and month that the page was
//      published, if file pattern includes it.
//   {{.URI}}: URI to the page.
//   {{.IsLive}}: Whether the page is live, via !appengine.IsDevAppServer().
package autosite

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"appengine"
)

// New creates a new autosite.
//
// New panics on errors reading templates.
func New(title, glob, live string, templates []string) Site {
	s := Site{
		title:     title,
		live:      live,
		glob:      glob,
		templates: templates,
	}
	err := s.read()
	if err != nil {
		log.Fatalf(err.Error())
	}
	return s
}

// ChangeURI changes the URI a page will be served on.
//
// ChangeURI panics if the old URI is not registered.
func (s *Site) ChangeURI(uri, newURI string) {
	p, ok := s.pages[uri]
	if !ok {
		log.Fatalf("no page with URI %v\n", uri)
	}
	p.URI = newURI
	delete(s.pages, uri)
	s.pages[newURI] = p
	log.Printf("remapped %v to %v\n", p, newURI)
}

// Register registers the HTTP handlers for the site.
func (s Site) Register() {
	for uri, p := range s.pages {
		if appengine.IsDevAppServer() {
			http.Handle(uri, p)
		} else {
			http.Handle(fmt.Sprintf("%s%s", s.live, p.URI), p)
		}
		log.Printf("registered handler %s: %+v\n", p.URI, p)
	}
}

// Site represents an autosite.
type Site struct {
	live      string          // live domain
	title     string          // title of the domain, for HTML <head>
	glob      string          // file glob for page templates
	templates []string        // templates needed for all endpoints
	pages     map[string]page // URI -> page mapping
}

// page is a HTML resource.
type page struct {
	Title  string      // title, for <head>
	Date   date        // publishing date
	URI    string      // URI path
	IsLive bool        // true if the site is live
	Data   interface{} // custom data, if any

	tmpl *template.Template // backing template
}

type year int

// date is a rough point in time.
type date struct {
	Year  year
	Month time.Month
}

// before says whether this date is before other date.
func (d date) before(other date) bool {
	if d.Year < other.Year {
		return true
	} else if d.Year == other.Year {
		return d.Month < other.Month
	}
	return false
}

// ServeHTTP serves the page.
func (p page) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	c.Infof("%+v will ServeHTTP for URI %s\n", p, r.RequestURI)

	if p.URI != r.RequestURI {
		c.Errorf("bad request URI %s, want %s; serving 404\n", r.RequestURI, p.URI)
		http.NotFound(w, r)
		return
	}

	err := p.tmpl.ExecuteTemplate(w, "base", p)
	if err != nil {
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		log.Fatal(err.Error())
		return
	}
}

// String provides a string representation of the page.
func (p page) String() string {
	r := fmt.Sprintf("page [%s]", p.URI)
	if p.Date.Year != 0 {
		r += fmt.Sprintf(", published on %v", p.Date.Year)
		if p.Date.Month != 0 {
			r += fmt.Sprintf(", %v", p.Date.Month)
		}
	}
	return r
}

// read reads pages to serve on the autosite from disk
func (s *Site) read() error {
	filePaths, err := s.getFiles()
	if err != nil {
		return err
	}
	s.pages = make(map[string]page)
	for _, tmplPath := range filePaths {
		uri, d, err := parsePath(tmplPath)
		if err != nil {
			return err
		}
		s.addPage(uri, d, nil, append(s.templates, tmplPath))
	}
	return nil
}

// getFiles retrieves all pages' file paths from disk.
func (s Site) getFiles() ([]string, error) {
	paths, err := filepath.Glob(s.glob)
	if err != nil {
		return []string{}, err
	}
	if len(paths) == 0 {
		return []string{}, fmt.Errorf("no pages found")
	}

	// Skip files with dot prefixes (e.g. .#foo.tmpl).
	r := make([]string, len(paths))
	i := 0
	for _, p := range paths {
		if strings.Contains(p, ".#") {
			continue
		}
		r[i] = p
		i++
	}
	return r[0:i], nil
}

// parsePath extracts URI and date from a template file path.
func parsePath(p string) (uri string, d date, err error) {
	parts := strings.Split(p, "/")
	if len(parts) == 2 {
		// Assumes [dir]/*.tmpl; i.e. no date.
		uri = fmt.Sprintf("/%s", strings.TrimSuffix(parts[1], ".tmpl"))
	} else if len(parts) == 4 {
		// Assumes [dir]/[yyyy]/[mm]/*.tmpl; i.e. date is present.
		uri = "/" + strings.Join([]string{
			parts[1],
			parts[2],
			strings.TrimSuffix(parts[3], ".tmpl")}, "/")
		d, err = getDate(parts[1], parts[2])
		if err != nil {
			return
		}
	} else {
		err = fmt.Errorf("bad template path: %s", p)
		return
	}
	return uri, d, nil
}

// getDate extracts the date of the post from year and month strings.
func getDate(y, m string) (date, error) {
	y64, err := strconv.ParseInt(y, 10, 0)
	if err != nil || y64 <= 1900 || y64 >= 99999 {
		return date{}, fmt.Errorf("bad year: %v", y)
	}
	month, err := strconv.ParseInt(m, 10, 0)
	if err != nil || month < 1 || month > 12 {
		return date{}, fmt.Errorf("bad month: %v", m)
	}
	return date{
		Year:  year(y64),
		Month: time.Month(month),
	}, nil
}

// addPage adds a page to the autosite.
func (s *Site) addPage(uri string, d date, data interface{}, tmpls []string) {
	t := template.Must(template.ParseFiles(tmpls...))
	p := page{
		Title:  s.title,
		URI:    uri,
		Data:   data,
		Date:   d,
		IsLive: !appengine.IsDevAppServer(),
		tmpl:   t,
	}
	s.pages[p.URI] = p
}

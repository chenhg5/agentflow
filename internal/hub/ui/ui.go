// Package ui renders the Hub's pages: server-side HTML with an embedded
// stylesheet taken from the Loops console's look, and no build step.
package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/api"
	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Pages serves the HTML pages.
type Pages struct {
	Query   app.Query
	Version string
	pages   map[string]*template.Template
}

// assetURLs maps each static file to /static/<name>?v=<content hash>, so a
// browser never pairs a new page with a stylesheet or script it cached
// before the Hub was upgraded.
func assetURLs() map[string]string {
	urls := map[string]string{}
	fs.WalkDir(staticFS, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		name := strings.TrimPrefix(path, "static/")
		urls[name] = "/static/" + name + "?v=" + hex.EncodeToString(sum[:6])
		return nil
	})
	return urls
}

// New parses the templates once.
func New(query app.Query, version string) (*Pages, error) {
	assets := assetURLs()
	helpers := funcs()
	helpers["asset"] = func(name string) string {
		if url, ok := assets[name]; ok {
			return url
		}
		return "/static/" + name
	}
	base, err := template.New("").Funcs(helpers).ParseFS(templateFS, "templates/layout.html", "templates/parts.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"overview", "runs", "run", "workflows", "machines", "missing"} {
		page, err := template.Must(base.Clone()).ParseFS(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = page
	}
	return &Pages{Query: query, Version: version, pages: pages}, nil
}

// Register mounts the pages and static files behind view.
func (p *Pages) Register(mux *http.ServeMux, view api.Guard) {
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(static)))))
	mux.Handle("GET /{$}", view(http.HandlerFunc(p.overview)))
	mux.Handle("GET /runs", view(http.HandlerFunc(p.runs)))
	mux.Handle("GET /runs/{machine}/{run}", view(http.HandlerFunc(p.run)))
	mux.Handle("GET /workflows", view(http.HandlerFunc(p.workflows)))
	mux.Handle("GET /machines", view(http.HandlerFunc(p.machines)))
}

// Crumb is one step of the breadcrumb.
type Crumb struct {
	Label string
	Href  string
}

// page is what the layout needs around every page's own data.
type page struct {
	Title   string
	Tab     string
	Crumbs  []Crumb
	Live    bool
	Version string
	Data    any
}

var dayChoices = []int{7, 14, 30}

type overviewData struct {
	app.Overview
	Chart   template.HTML
	Choices []int
	Rate    float64
}

func (p *Pages) overview(w http.ResponseWriter, r *http.Request) {
	days := api.Days(r, 14)
	overview, err := p.Query.Overview(r.Context(), days)
	if err != nil {
		p.fail(w, err)
		return
	}
	p.render(w, http.StatusOK, "overview", page{Title: "总览", Tab: "overview", Live: true, Crumbs: []Crumb{{Label: "总览"}},
		Data: overviewData{Overview: overview, Chart: runsChart(overview.Daily), Choices: dayChoices, Rate: overview.Counts.SuccessRate()}})
}

type statusTab struct {
	Label  string
	Status string
	Href   string
}

var statusTabs = []statusTab{{Label: "全部"}, {Label: "运行中", Status: "running"}, {Label: "等人处理", Status: "waiting_human,waiting_input"}, {Label: "失败", Status: "failed"}, {Label: "成功", Status: "succeeded"}, {Label: "已取消", Status: "cancelled"}}

type runsData struct {
	Runs      []domain.Run
	Total     int
	Filter    app.RunFilter
	Status    string
	Tabs      []statusTab
	Workflows []string
	Machines  []string
	Stat      *app.WorkflowStat
	PrevHref  string
	NextHref  string
	From      int
	To        int
}

// runsHref is the runs page with the current filters, some of them changed;
// an empty value removes that parameter.
func runsHref(current url.Values, changes ...string) string {
	values := url.Values{}
	for key, list := range current {
		values[key] = append([]string(nil), list...)
	}
	for index := 0; index+1 < len(changes); index += 2 {
		if changes[index+1] == "" {
			values.Del(changes[index])
		} else {
			values.Set(changes[index], changes[index+1])
		}
	}
	if len(values) == 0 {
		return "/runs"
	}
	return "/runs?" + values.Encode()
}

const pageSize = 50

func (p *Pages) runs(w http.ResponseWriter, r *http.Request) {
	filter, err := api.Filter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter.Limit = pageSize
	runs, total, err := p.Query.Runs(r.Context(), filter)
	if err != nil {
		p.fail(w, err)
		return
	}
	stats, err := p.Query.Workflows(r.Context(), 30)
	if err != nil {
		p.fail(w, err)
		return
	}
	machines, err := p.Query.Machines(r.Context())
	if err != nil {
		p.fail(w, err)
		return
	}
	current := r.URL.Query()
	data := runsData{Runs: runs, Total: total, Filter: filter, Status: current.Get("status"),
		From: filter.Offset + 1, To: filter.Offset + len(runs)}
	for _, tab := range statusTabs {
		tab.Href = runsHref(current, "status", tab.Status, "offset", "")
		data.Tabs = append(data.Tabs, tab)
	}
	for index := range stats {
		data.Workflows = append(data.Workflows, stats[index].Name)
		if stats[index].Name == filter.Workflow {
			data.Stat = &stats[index]
		}
	}
	sort.Strings(data.Workflows)
	for _, machine := range machines {
		data.Machines = append(data.Machines, machine.ID)
	}
	sort.Strings(data.Machines)
	if filter.Offset > 0 {
		previous := ""
		if offset := max(filter.Offset-pageSize, 0); offset > 0 {
			previous = itoa(offset)
		}
		data.PrevHref = runsHref(current, "offset", previous)
	}
	if filter.Offset+len(runs) < total {
		data.NextHref = runsHref(current, "offset", itoa(filter.Offset+pageSize))
	}
	crumbs := []Crumb{{Label: "运行", Href: "/runs"}}
	if filter.Workflow != "" {
		crumbs = append(crumbs, Crumb{Label: filter.Workflow})
	}
	p.render(w, http.StatusOK, "runs", page{Title: "运行", Tab: "runs", Live: filter.Offset == 0, Crumbs: crumbs, Data: data})
}

type runData struct {
	Run      domain.Run
	Flow     string
	Timeline []timelineRow
	Done     int
	Failed   *domain.Step
}

func (p *Pages) run(w http.ResponseWriter, r *http.Request) {
	key := domain.RunKey{Machine: r.PathValue("machine"), RunID: r.PathValue("run")}
	run, err := p.Query.Run(r.Context(), key)
	if errors.Is(err, app.ErrNotFound) {
		p.render(w, http.StatusNotFound, "missing", page{Title: "找不到这次运行", Tab: "runs",
			Crumbs: []Crumb{{Label: "运行", Href: "/runs"}, {Label: key.RunID}}, Data: key})
		return
	}
	if err != nil {
		p.fail(w, err)
		return
	}
	data := runData{Run: run, Flow: flowJSON(run), Timeline: timeline(run, clock())}
	for index, step := range run.Steps {
		if step.Status == domain.StatusSucceeded || step.Status == domain.StatusSkipped {
			data.Done++
		}
		if step.Status == domain.StatusFailed && data.Failed == nil {
			data.Failed = &run.Steps[index]
		}
	}
	p.render(w, http.StatusOK, "run", page{Title: run.Workflow + " · " + run.Key.RunID, Tab: "runs", Live: !run.Status.Finished(),
		Crumbs: []Crumb{{Label: "运行", Href: "/runs"}, {Label: run.Workflow, Href: "/runs?workflow=" + url.QueryEscape(run.Workflow)}, {Label: shortID(run.Key.RunID)}},
		Data:   data})
}

type workflowsData struct {
	Workflows []app.WorkflowStat
	Days      int
	Choices   []int
}

func (p *Pages) workflows(w http.ResponseWriter, r *http.Request) {
	days := api.Days(r, 30)
	stats, err := p.Query.Workflows(r.Context(), days)
	if err != nil {
		p.fail(w, err)
		return
	}
	sort.SliceStable(stats, func(i, j int) bool { return stats[i].Name < stats[j].Name })
	p.render(w, http.StatusOK, "workflows", page{Title: "Workflow", Tab: "workflows", Crumbs: []Crumb{{Label: "Workflow"}},
		Data: workflowsData{Workflows: stats, Days: days, Choices: []int{7, 30, 90}}})
}

type machineRow struct {
	domain.Machine
	Online bool
}

func (p *Pages) machines(w http.ResponseWriter, r *http.Request) {
	machines, err := p.Query.Machines(r.Context())
	if err != nil {
		p.fail(w, err)
		return
	}
	rows := make([]machineRow, len(machines))
	for index, machine := range machines {
		rows[index] = machineRow{Machine: machine, Online: clock().Sub(machine.LastSeen) < 15*time.Minute}
	}
	p.render(w, http.StatusOK, "machines", page{Title: "机器", Tab: "machines", Crumbs: []Crumb{{Label: "机器"}}, Data: rows})
}

func (p *Pages) render(w http.ResponseWriter, status int, name string, data page) {
	data.Version = p.Version
	var body bytes.Buffer
	if err := p.pages[name].ExecuteTemplate(&body, "layout", data); err != nil {
		p.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(body.Bytes())
}

func (p *Pages) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	log.Printf("hub page: %v", err)
	http.Error(w, "hub error: "+err.Error(), http.StatusInternalServerError)
}

// cacheStatic lets browsers keep fingerprinted URLs (?v=) for a year and
// makes them revalidate anything else.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

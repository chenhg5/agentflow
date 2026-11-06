// Package ui renders the Hub's pages: server-side HTML with an embedded
// stylesheet taken from the Loops console's look, and no build step.
package ui

import (
	"bytes"
	"context"
	"crypto/rand"
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
	Query    app.Query
	Commands app.Commands
	Version  string
	pages    map[string]*template.Template
	csrfKey  []byte
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
func New(query app.Query, commands app.Commands, version string) (*Pages, error) {
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
	for _, name := range []string{"overview", "runs", "run", "workflows", "workflow", "machines", "missing"} {
		page, err := template.Must(base.Clone()).ParseFS(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = page
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &Pages{Query: query, Commands: commands, Version: version, pages: pages, csrfKey: key}, nil
}

// Register mounts the pages and static files behind view.
func (p *Pages) Register(mux *http.ServeMux, view api.Guard) {
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(static)))))
	mux.Handle("GET /{$}", view(http.HandlerFunc(p.overview)))
	mux.Handle("GET /runs", view(http.HandlerFunc(p.runs)))
	mux.Handle("GET /runs/{machine}/{run}", view(http.HandlerFunc(p.run)))
	mux.Handle("POST /runs/{machine}/{run}/submit", view(api.SameOrigin(http.HandlerFunc(p.submit))))
	mux.Handle("GET /workflows", view(http.HandlerFunc(p.workflows)))
	mux.Handle("GET /workflows/{name}", view(http.HandlerFunc(p.workflow)))
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
	Workflows []app.WorkflowStat
	Groups    []string
	Tags      []app.TagCount
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
	stats, err := p.Query.Workflows(r.Context(), 30, nil, "")
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
		data.Workflows = append(data.Workflows, stats[index])
		if stats[index].Name == filter.Workflow {
			data.Stat = &stats[index]
		}
	}
	sort.SliceStable(data.Workflows, func(i, j int) bool { return data.Workflows[i].Label() < data.Workflows[j].Label() })
	for _, group := range app.GroupWorkflows(stats) {
		if group.Name != "" {
			data.Groups = append(data.Groups, group.Name)
		}
	}
	data.Tags = app.Tags(stats)
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
	if data.Stat != nil {
		crumbs = append(crumbs, Crumb{Label: data.Stat.Label(), Href: "/workflows/" + url.PathEscape(data.Stat.Name)})
	} else if filter.Workflow != "" {
		crumbs = append(crumbs, Crumb{Label: filter.Workflow})
	}
	p.render(w, http.StatusOK, "runs", page{Title: "运行", Tab: "runs", Live: filter.Offset == 0, Crumbs: crumbs, Data: data})
}

type runData struct {
	Run      domain.Run
	Inputs   []inputForm
	Commands []domain.Command
	Notice   string
	Problem  string
	Flow     string
	Timeline []timelineRow
	Done     int
	Failed   *domain.Step
}

func (p *Pages) run(w http.ResponseWriter, r *http.Request) {
	key := domain.RunKey{Machine: r.PathValue("machine"), RunID: r.PathValue("run")}
	detail, err := p.Query.Detail(r.Context(), key)
	run := detail.Run
	if errors.Is(err, app.ErrNotFound) {
		p.render(w, http.StatusNotFound, "missing", page{Title: "找不到这次运行", Tab: "runs",
			Crumbs: []Crumb{{Label: "运行", Href: "/runs"}, {Label: key.RunID}}, Data: missingData{Heading: "找不到这次运行",
				Message: "机器 " + key.Machine + " 上没有报过 " + key.RunID + "。可能还没上报，或者在那台机器上跑一次 agentflow hub push --all。",
				Back:    "/runs", BackLabel: "回到运行列表"}})
		return
	}
	if err != nil {
		p.fail(w, err)
		return
	}
	data := runData{Run: run, Flow: flowJSON(run), Timeline: timeline(run, clock()), Notice: r.URL.Query().Get("notice"), Problem: r.URL.Query().Get("problem"),
		Inputs: p.inputForms(detail), Commands: detail.Commands}
	for index, step := range run.Steps {
		if step.Status == domain.StatusSucceeded || step.Status == domain.StatusSkipped {
			data.Done++
		}
		if step.Status == domain.StatusFailed && data.Failed == nil {
			data.Failed = &run.Steps[index]
		}
	}
	p.render(w, http.StatusOK, "run", page{Title: run.Label() + " · " + run.Key.RunID, Tab: "runs", Live: !run.Status.Finished(),
		Crumbs: []Crumb{{Label: "运行", Href: "/runs"}, {Label: run.Label(), Href: "/workflows/" + url.PathEscape(run.Workflow)}, {Label: shortID(run.Key.RunID)}},
		Data:   data})
}

type workflowsData struct {
	Workflows []app.WorkflowStat
	Groups    []string
	Tags      []app.TagCount
	Tag       string
	Group     string
	Days      int
	Choices   []int
}

func (p *Pages) workflows(w http.ResponseWriter, r *http.Request) {
	days := api.Days(r, 30)
	tags, group := api.Tags(r), strings.TrimSpace(r.URL.Query().Get("group"))
	stats, err := p.Query.Workflows(r.Context(), days, tags, group)
	if err != nil {
		p.fail(w, err)
		return
	}
	// The group tabs and tag menu list every group and tag of the window, so
	// picking one keeps the others in reach.
	all := stats
	if len(tags) > 0 || group != "" {
		if all, err = p.Query.Workflows(r.Context(), days, nil, ""); err != nil {
			p.fail(w, err)
			return
		}
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Group != stats[j].Group {
			return stats[j].Group == "" || (stats[i].Group != "" && stats[i].Group < stats[j].Group)
		}
		return stats[i].Label() < stats[j].Label()
	})
	data := workflowsData{Workflows: stats, Tags: app.Tags(all), Group: group, Days: days, Choices: []int{7, 30, 90}}
	for _, section := range app.GroupWorkflows(all) {
		if section.Name != "" {
			data.Groups = append(data.Groups, section.Name)
		}
	}
	if len(tags) > 0 {
		data.Tag = tags[0]
	}
	crumbs := []Crumb{{Label: "Workflow", Href: "/workflows"}}
	if group != "" {
		crumbs = append(crumbs, Crumb{Label: group})
	}
	p.render(w, http.StatusOK, "workflows", page{Title: "Workflow", Tab: "workflows", Crumbs: crumbs, Data: data})
}

type missingData struct{ Heading, Message, Back, BackLabel string }

type workflowData struct {
	app.WorkflowDetail
	Flow    string
	Choices []int
}

func (p *Pages) workflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	days := api.Days(r, 30)
	detail, err := p.Query.Workflow(r.Context(), name, days)
	if errors.Is(err, app.ErrNotFound) {
		p.render(w, http.StatusNotFound, "missing", page{Title: "找不到这个 workflow", Tab: "workflows",
			Crumbs: []Crumb{{Label: "Workflow", Href: "/workflows"}, {Label: name}}, Data: missingData{Heading: "找不到这个 workflow",
				Message: "Hub 上还没有 " + name + " 的运行。它跑过一次并上报后就会出现在这里。", Back: "/workflows", BackLabel: "回到 Workflow 列表"}})
		return
	}
	if err != nil {
		p.fail(w, err)
		return
	}
	data := workflowData{WorkflowDetail: detail, Choices: []int{7, 30, 90}}
	if detail.Latest != nil {
		data.Flow = flowJSON(*detail.Latest)
	}
	crumbs := []Crumb{{Label: "Workflow", Href: "/workflows"}}
	if group := detail.Stat.Group; group != "" {
		crumbs = append(crumbs, Crumb{Label: group, Href: "/workflows?group=" + url.QueryEscape(group)})
	}
	crumbs = append(crumbs, Crumb{Label: detail.Stat.Label()})
	p.render(w, http.StatusOK, "workflow", page{Title: detail.Stat.Label(), Tab: "workflows", Live: true, Crumbs: crumbs, Data: data})
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

// inputForms builds a form for each step of the run that waits for input.
func (p *Pages) inputForms(detail app.RunDetail) []inputForm {
	var forms []inputForm
	for _, input := range detail.Inputs {
		form := inputForm{NodeID: input.NodeID, Executor: input.Executor, Accepts: input.Accepts, CSRF: p.csrfToken(detail.Run.Key)}
		if !input.Since.IsZero() {
			form.Waiting = duration(clock().Sub(input.Since).Milliseconds())
		}
		form.Fields, form.RawJSON = formFor(input.OutputSchema)
		forms = append(forms, form)
	}
	return forms
}

// submit takes the input form of a waiting step and queues it for the runner.
func (p *Pages) submit(w http.ResponseWriter, r *http.Request) {
	key := domain.RunKey{Machine: r.PathValue("machine"), RunID: r.PathValue("run")}
	back := runURL(domain.Run{Key: key})
	if err := r.ParseForm(); err != nil || !p.validCSRF(key, r.PostForm.Get("csrf")) {
		http.Error(w, "表单已过期，请刷新页面后再提交", http.StatusForbidden)
		return
	}
	run, err := p.Query.Run(r.Context(), key)
	if err != nil {
		p.fail(w, err)
		return
	}
	nodeID := r.PostForm.Get("node")
	var schema map[string]any
	if run.Graph != nil {
		for _, node := range run.Graph.Nodes {
			if node.ID == nodeID {
				schema = node.OutputSchema
			}
		}
	}
	output, err := outputFromForm(r.PostForm, schema)
	if err != nil {
		http.Redirect(w, r, back+"?problem="+url.QueryEscape(err.Error())+"#input", http.StatusSeeOther)
		return
	}
	if _, err := p.Commands.Submit(r.Context(), key, nodeID, output, api.Who(r)); err != nil {
		http.Redirect(w, r, back+"?problem="+url.QueryEscape(submitProblem(err))+"#input", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?notice="+url.QueryEscape("已提交，等 "+key.Machine+" 取走执行")+"#input", http.StatusSeeOther)
}

func submitProblem(err error) string {
	switch {
	case errors.Is(err, domain.ErrNotWaiting):
		return "这一步已经不在等输入了"
	case errors.Is(err, domain.ErrNoCapability):
		return "这台机器没有开启从 Hub 接收输入"
	default:
		return err.Error()
	}
}

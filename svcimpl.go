package service

import (
	"context"
	"fmt"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/unionj-cloud/go-doudou/stringutils"
	"github.com/unionj-cloud/rabida/config"
	"github.com/unionj-cloud/rabida/internal/lib"
	"time"
)

type RabidaImpl struct {
	conf *config.RabiConfig
}

func (r RabidaImpl) CrawlWithConfig(ctx context.Context, job Job, callback func(ret []interface{}, nextPageUrl string, currentPageNo int) bool, before []chromedp.Action, after []chromedp.Action, conf config.RabiConfig, options ...chromedp.ExecAllocatorOption) error {
	var (
		err         error
		abort       bool
		ret         []interface{}
		nextPageUrl string
		pageNo      int
		cancel      context.CancelFunc
		timeoutCtx  context.Context
	)
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoSandbox,
	}
	opts = append(opts, options...)
	if conf.Mode == "headless" {
		opts = append(opts, chromedp.Headless)
	}

	ctx, cancel = chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	ctx, cancel = chromedp.NewContext(ctx)
	defer cancel()

	link := job.Link
	if stringutils.IsNotEmpty(job.StartPageUrl) {
		link = job.StartPageUrl
	}

	var tasks chromedp.Tasks

	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		_, err = page.AddScriptToEvaluateOnNewDocument(lib.Script).Do(ctx)
		if err != nil {
			return err
		}
		return nil
	}))

	tasks = append(tasks, before...)

	if err = chromedp.Run(ctx, tasks); err != nil {
		return errors.Wrap(err, "")
	}

	tasks = nil
	tasks = append(tasks, lib.Navigate(link))
	tasks = append(tasks, after...)

	timeoutCtx, cancel = context.WithTimeout(ctx, conf.Timeout)
	defer cancel()

	if err = chromedp.Run(timeoutCtx, tasks); err != nil {
		return errors.Wrap(err, "")
	}

	time.Sleep(conf.Timeout)

	if stringutils.IsNotEmpty(job.StartPageBtn.Css) {
		var father *cdp.Node
		if job.CssSelector.Iframe {
			if father, err = iframe(ctx, conf.Timeout); err != nil {
				return errors.Wrap(err, "")
			}
		}
		timeoutCtx, cancel = context.WithTimeout(ctx, conf.Timeout)
		defer cancel()
		if father != nil {
			if err = chromedp.Run(timeoutCtx, chromedp.Click(job.StartPageBtn.Css, chromedp.ByQuery, chromedp.FromNode(father))); err != nil {
				return errors.Wrap(err, "")
			}
		} else {
			if err = chromedp.Run(timeoutCtx, chromedp.Click(job.StartPageBtn.Css, chromedp.ByQuery)); err != nil {
				return errors.Wrap(err, "")
			}
		}
		time.Sleep(conf.Timeout)
	}

	ret, nextPageUrl, err = r.extract(ctx, job, conf)
	if err != nil {
		return errors.Wrap(err, "")
	}

	pageNo++
	if abort = callback(ret, nextPageUrl, pageNo); abort {
		return nil
	}

	if stringutils.IsEmpty(job.Paginator.Css) {
		return nil
	}

	r.sleep(conf)

	for {
		var father *cdp.Node
		if job.CssSelector.Iframe {
			if father, err = iframe(ctx, conf.Timeout); err != nil {
				return errors.Wrap(err, "")
			}
		}
		timeoutCtx, cancel = context.WithTimeout(ctx, conf.Timeout)
		if father != nil {
			if err = chromedp.Run(timeoutCtx, chromedp.Click(job.Paginator.Css, chromedp.ByQuery, chromedp.FromNode(father))); err != nil {
				goto ERR
			}
		} else {
			if err = chromedp.Run(timeoutCtx, chromedp.Click(job.Paginator.Css, chromedp.ByQuery)); err != nil {
				goto ERR
			}
		}
		time.Sleep(conf.Timeout)
		if ret, nextPageUrl, err = r.extract(ctx, job, conf); err != nil {
			goto ERR
		}
		pageNo++
		if abort = callback(ret, nextPageUrl, pageNo); abort {
			goto END
		}
		cancel()

		r.sleep(conf)
		continue

	END:
		cancel()
		return nil
	ERR:
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return errors.Wrap(err, "")
	}
}

func iframe(ctx context.Context, timeout time.Duration) (iframe *cdp.Node, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var iframes []*cdp.Node
	if err = chromedp.Run(timeoutCtx, chromedp.Nodes("iframe", &iframes, chromedp.ByQuery)); err != nil {
		return nil, errors.Wrap(err, "")
	}
	if len(iframes) > 0 {
		iframe = iframes[0]
	}
	return
}

func (r RabidaImpl) sleep(conf config.RabiConfig) {
	var s time.Duration
	if len(conf.Delay) > 1 {
		s = lib.RandDuration(conf.Delay[0], conf.Delay[1])
	} else {
		s = conf.Delay[0]
	}
	logrus.Infof("sleep %s to crawl next page\n", s.String())
	time.Sleep(s)
}

func (r RabidaImpl) Crawl(ctx context.Context, job Job, callback func(ret []interface{}, nextPageUrl string, currentPageNo int) bool,
	before []chromedp.Action, after []chromedp.Action) error {
	return r.CrawlWithConfig(ctx, job, callback, before, after, *r.conf)
}

type errNotFound struct{}

func (e errNotFound) Error() string {
	return "not found"
}

var ErrNotFound error = errNotFound{}

func (r RabidaImpl) populate(ctx context.Context, father *cdp.Node, cssSelector CssSelector, conf config.RabiConfig) []interface{} {
	scope := cssSelector.Scope
	if stringutils.IsEmpty(scope) && father == nil {
		scope = "html"
	}
	var nodes []*cdp.Node
	if stringutils.IsNotEmpty(scope) {
		timeoutCtx, cancel := context.WithTimeout(ctx, conf.Timeout)
		defer cancel()
		if father != nil {
			if err := chromedp.Run(timeoutCtx, chromedp.Nodes(scope, &nodes, chromedp.ByQueryAll, chromedp.FromNode(father))); err != nil {
				logrus.Error(fmt.Sprintf("%+v", errors.Wrap(ErrNotFound, scope)))
			}
		} else {
			if err := chromedp.Run(timeoutCtx, chromedp.Nodes(scope, &nodes, chromedp.ByQueryAll)); err != nil {
				logrus.Error(fmt.Sprintf("%+v", errors.Wrap(ErrNotFound, scope)))
			}
		}
	} else {
		nodes = append(nodes, father)
	}
	var ret []interface{}
	for _, node := range nodes {
		if cssSelector.Attrs == nil {
			timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			var value string
			if stringutils.IsEmpty(cssSelector.Attr) {
				if stringutils.IsEmpty(cssSelector.Css) {
					_ = chromedp.Run(timeoutCtx, chromedp.JavascriptAttribute([]cdp.NodeID{node.NodeID}, "innerText", &value, chromedp.ByNodeID))
				} else {
					var _nodes []*cdp.Node
					_ = chromedp.Run(timeoutCtx, chromedp.Nodes(cssSelector.Css, &_nodes, chromedp.ByQueryAll, chromedp.FromNode(node)))
					for _, _node := range _nodes {
						var temp string
						_ = chromedp.Run(timeoutCtx, chromedp.JavascriptAttribute([]cdp.NodeID{_node.NodeID}, "innerText", &temp, chromedp.ByNodeID))
						value += temp
					}
				}
			} else {
				if stringutils.IsEmpty(cssSelector.Css) {
					if cssSelector.Attr == "outerHTML" {
						_ = chromedp.Run(timeoutCtx, chromedp.OuterHTML([]cdp.NodeID{node.NodeID}, &value, chromedp.ByNodeID))
					} else if cssSelector.Attr == "innerHTML" {
						_ = chromedp.Run(timeoutCtx, chromedp.InnerHTML([]cdp.NodeID{node.NodeID}, &value, chromedp.ByNodeID))
					} else {
						_ = chromedp.Run(timeoutCtx, chromedp.JavascriptAttribute([]cdp.NodeID{node.NodeID}, cssSelector.Attr, &value, chromedp.ByNodeID))
					}
				} else {
					if cssSelector.Attr == "outerHTML" {
						_ = chromedp.Run(timeoutCtx, chromedp.OuterHTML(cssSelector.Css, &value, chromedp.ByQuery, chromedp.FromNode(node)))
					} else if cssSelector.Attr == "innerHTML" {
						_ = chromedp.Run(timeoutCtx, chromedp.InnerHTML(cssSelector.Css, &value, chromedp.ByQuery, chromedp.FromNode(node)))
					} else if cssSelector.Attr == "innerText" {
						var _nodes []*cdp.Node
						_ = chromedp.Run(timeoutCtx, chromedp.Nodes(cssSelector.Css, &_nodes, chromedp.ByQueryAll, chromedp.FromNode(node)))
						for _, _node := range _nodes {
							var temp string
							_ = chromedp.Run(timeoutCtx, chromedp.JavascriptAttribute([]cdp.NodeID{_node.NodeID}, "innerText", &temp, chromedp.ByNodeID))
							value += temp
						}
					} else {
						_ = chromedp.Run(timeoutCtx, chromedp.JavascriptAttribute(cssSelector.Css, cssSelector.Attr, &value, chromedp.ByQuery, chromedp.FromNode(node)))
					}
				}
			}
			if stringutils.IsNotEmpty(value) {
				ret = append(ret, value)
			}
			cancel()
		} else {
			data := make(map[string]interface{})
			for attr, sel := range cssSelector.Attrs {
				result := r.populate(ctx, node, sel, conf)
				if len(result) > 0 {
					if stringutils.IsEmpty(sel.Scope) {
						data[attr] = result[0]
					} else {
						data[attr] = result
					}
				}
			}
			if len(data) == 0 {
				continue
			}
			ret = append(ret, data)
		}
	}
	return ret
}

func (r RabidaImpl) extract(ctx context.Context, job Job, conf config.RabiConfig) (ret []interface{}, nextPageUrl string, err error) {
	defer func() {
		if val := recover(); val != nil {
			var ok bool
			err, ok = val.(error)
			if !ok {
				err = errors.New(fmt.Sprint(val))
			} else {
				err = errors.Wrap(err, "recover from panic")
			}
		}
	}()

	var father *cdp.Node
	if job.CssSelector.Iframe {
		if father, err = iframe(ctx, conf.Timeout); err != nil {
			panic(errors.Wrap(err, ""))
		}
	}
	ret = r.populate(ctx, father, job.CssSelector, conf)
	timeoutCtx, cancel := context.WithTimeout(ctx, conf.Timeout)
	defer cancel()
	_ = chromedp.Run(timeoutCtx, chromedp.JavascriptAttribute(job.Paginator.Css, job.Paginator.Attr, &nextPageUrl, chromedp.ByQuery))
	return
}

func NewRabida(conf *config.RabiConfig) Rabida {
	return &RabidaImpl{
		conf,
	}
}

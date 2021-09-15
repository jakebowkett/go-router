/*
Package router is a simple HTTP router.

It allows adding routes according to HTTP verbs (GET, POST, PUT, etc).
Patterns supplied may have segments that use variable names which can
serve as wildcards or they can specify a list of acceptable segments.

	rt := router.New(router.Options{})

	// Literal routes.
	rt.Get("/about", handler.Page)
	rt.Get("/contact", handler.Page)

	// Wildcard route.
	rt.Get("/:page", handler.Page)

	// Limited wildcard.
	rt.Get("/:page[about,contact]", handler.Page)

	// Combination of all three.
	rt.Get("/:section[user,item]/:id/summary", handler.Summary)

Middleware can be placed between routes:

	rt.Get("/static/:file", handler.Static)

	rt.Use(func(w http.ResponseWriter, r *router.Request) {
		u, err := database.User(r.Request.Cookie)
		if err != nil {
			// handle
		}
		r.User = u
	})

	// Response may change depending on whether or not
	// a user account was attached to the request.
	rt.Get("/home", handler.Page)

Routes can be grouped and use guards that allow skipping over groups of
routes entirely without acknowledging they exist or more transparently
acknowledging them but denying access.

	rt.Get("/home", handler.Page)

	// This call to rt.Group supplies a function whose response
	// determines whether the request is authorized to access
	// the group. If the pattern matches but access is denied the
	// Error function supplied to Router by its Options will be
	// called with the Request struct's Status field set to
	// http.StatusUnauthorized.
	acc := rt.Group("/account", nil, func(r *router.Request) bool {
		return r.User == nil
	})
	acc.Get("/", handler.Account)
	acc.Get("/messages", handler.Messages)
*/
package router

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Handler = func(w http.ResponseWriter, r *Request)
type Guard = func(r *Request) bool
type Vars = map[string]string
type Request = struct {
	Id      string
	Request *http.Request
	Vars    Vars
	User    interface{}
	Status  int
	Error   error
	Began   time.Time
}

type Options = struct {
	/*
		Error is called if the router encounters an error
		while handling requests. The *Request supplied to
		this Handler will have its Error and Status fields
		populated with the relevant error and status code
		respectively.

		Note that Error isn't called if there is an error
		while adding routes. See New for more information.
	*/
	Error Handler

	/*
		Recover will be called in the event of a panic. The
		supplied *Request will contain the error in its Error
		field. Recover is called immediatley before Deferred,
		if the latter is present.
	*/
	Recover Handler

	/*
		Deferred is called at the end of request processing.
		The Began field of the supplied *Request can be used
		to calculate the rough time the request has taken.
	*/
	Deferred Handler
}

type Router struct {
	route
	opt     Options
	reqId   uint64
	reqIdMu sync.Mutex
	Errors  []error
}

/*
New returns an initialised *Router that is ready to have
routes added to it. The returned *Router has an Errors
field that will be populated with errors resulting from
calls to its methods named after the HTTP verbs (Get, Pst,
Put, etc.)
*/
func New(o Options) *Router {
	rt := &Router{}
	rt.opt = o
	rt.route.rt = rt
	return rt
}

func (rt *Router) idGen() string {
	rt.reqIdMu.Lock()
	rt.reqId++
	n := rt.reqId
	rt.reqIdMu.Unlock()
	return strconv.FormatUint(n, 36)
}

type route struct {
	method       string
	pattern      []segment
	route        []route
	handler      Handler
	use          Handler
	skip         Guard
	unauthorized Guard
	rt           *Router
}

type segment struct {
	raw     string
	varName string
	matches []string
}

/*
Use can be placed among calls to the HTTP verb methods
without affecting matches. Since it is supplied a pointer
to Request, one use for this method could be to attach
a user object to the User field of *Request.

If the handler sets the Status field of *Request to a 3xx
code the request will be terminated at the conclusion of
handler. It is the responsibility of the code within the
handler to actually do the redirect.

If the handler sets the Status field to an error code (4xx
or 5xx) the request will be prematurely terminated. It is
the responsibility of the handler to respond to the request.
The error must be assigned to the supplied *Request object's
Error field
*/
func (r *route) Use(handler Handler) {
	rt := r.rt
	if handler == nil {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			"function supplied to Use is nil",
		))
	}
	r.route = append(r.route, route{
		use: handler,
	})
}

/*
Group allows for groupings of routes.

The return value of skip determines if this grouping will
even be examined. If skip returns true the pattern matching
check will completely skip over the grouped routes as though
they don't exist. If skip returns false or is nil the patterns
within the grouping will be checked as usual.

The return value of unauthorized determines if the client has
authorisation to visit this grouping. Assuming skip is nil
or returns false, unauthorized always checks patterns for matches.
If unauthorized returns true AND a pattern matches then the parent
*Router Error will be called (if supplied) with the Status field
of *Request set to 401 (i.e., Unauthorized).
*/
func (r *route) Group(pattern string, skip, unauthorized Guard) *route {
	rt := r.rt
	group := route{
		pattern:      rt.expandPattern(pattern),
		skip:         skip,
		unauthorized: unauthorized,
		rt:           rt,
	}
	r.route = append(r.route, group)
	return &r.route[len(r.route)-1]
}

// HEAD
func (r *route) Hed(pattern string, handler Handler) {
	r.add("HEAD", pattern, handler)
}

// TRACE
func (r *route) Trc(pattern string, handler Handler) {
	r.add("TRACE", pattern, handler)
}

// CONNECT
func (r *route) Con(pattern string, handler Handler) {
	r.add("CONNECT", pattern, handler)
}

// OPTIONS
func (r *route) Opt(pattern string, handler Handler) {
	r.add("OPTIONS", pattern, handler)
}

// GET
func (r *route) Get(pattern string, handler Handler) {
	r.add("GET", pattern, handler)
}

// POST
func (r *route) Pst(pattern string, handler Handler) {
	r.add("POST", pattern, handler)
}

// PUT
func (r *route) Put(pattern string, handler Handler) {
	r.add("PUT", pattern, handler)
}

// PATCH
func (r *route) Pat(pattern string, handler Handler) {
	r.add("PATCH", pattern, handler)
}

// DELETE
func (r *route) Del(pattern string, handler Handler) {
	r.add("DELETE", pattern, handler)
}
func (r *route) add(method, pattern string, handler Handler) {

	rt := r.rt

	if handler == nil {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			"no handler supplied for route %s %s",
			method,
			pattern,
		))
	}

	if seenRoute(r.route, method, rt.expandPattern(pattern)) {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			"unreachable route due to duplicate method and pattern: %s %s",
			method,
			pattern,
		))
	}

	r.route = append(r.route, route{
		method:  method,
		pattern: rt.expandPattern(pattern),
		handler: handler,
		rt:      rt,
	})
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {

	r := &Request{
		Id:      rt.idGen(),
		Request: request,
		Vars:    make(Vars),
		Began:   time.Now(),
	}

	if rt.opt.Deferred != nil {
		defer rt.opt.Deferred(w, r)
	}

	/*
		This is after the call to rt.Deferred call because
		deferred calls are executed in reverse order.
	*/
	if rt.opt.Recover != nil {
		defer func() {
			if rec := recover(); rec != nil {
				r.Error = fmt.Errorf("%v", rec)
				rt.opt.Recover(w, r)
			}
		}()
	}

	reqPath := explodePath(request.URL.Path)
	code, terminate, match := iterateRoutes(w, r, rt.route.route, reqPath, false)
	if terminate {
		return
	}
	if rt.opt.Error == nil {
		return
	}
	if !match {
		code = 404
	}
	if code >= 400 {
		if r.Vars == nil {
			r.Vars = make(Vars)
		}
		r.Status = code
		rt.opt.Error(w, r)
		return
	}
}

/*
iterateRoutes recursively searches routes for the first match
to reqPath.
*/
func iterateRoutes(
	w http.ResponseWriter,
	r *Request,
	routes []route,
	reqPath []string,
	unauthorized bool,
) (
	code int,
	terminate bool,
	match bool,
) {
	for _, route := range routes {
		if route.use != nil {
			route.use(w, r)
			if r.Error != nil {
				return r.Status, true, false
			}
			if r.Status >= 300 && r.Status < 600 {
				return r.Status, true, false
			}
			continue
		}
		if route.skip != nil && route.skip(r) {
			continue
		}
		if route.method != "" && route.method != r.Request.Method {
			continue
		}
		if len(route.pattern) > len(reqPath) {
			continue
		}
		remainingPath := reqPath[len(route.pattern):]
		vars, ok := pathsMatch(route.pattern, reqPath[:len(route.pattern)])
		if !ok {
			continue
		}
		r.Vars = vars
		// Make a copy for this iteration so as to not affect sibling routes.
		unauthorized := unauthorized
		if route.unauthorized != nil && route.unauthorized(r) {
			unauthorized = true
		}
		if len(remainingPath) == 0 {
			if unauthorized {
				return http.StatusUnauthorized, false, true
			}
			route.handler(w, r)
			return 0, false, true
		}
		if len(route.route) > 0 {
			c, t, m := iterateRoutes(w, r, route.route, remainingPath, unauthorized)
			if t || m {
				return c, t, m
			}
		}
	}
	return 0, false, false
}

func seenRoute(routes []route, method string, pattern []segment) bool {
	for _, route := range routes {
		if route.use != nil {
			continue
		}
		if route.method != "" && route.method != method {
			continue
		}
		if len(route.pattern) > len(pattern) {
			continue
		}
		remainingPattern := pattern[len(route.pattern):]
		ok := routesMatchExactly(route.pattern, pattern[:len(route.pattern)])
		if !ok {
			continue
		}
		if len(remainingPattern) == 0 {
			return true
		}
		if len(route.route) > 0 {
			if seenRoute(route.route, method, remainingPattern) {
				return true
			}
		}
	}
	return false
}

func routesMatchExactly(existing, proposed []segment) bool {
	if len(existing) != len(proposed) {
		return false
	}
	for i, eSeg := range existing {
		pSeg := proposed[i]
		if eSeg.matches == nil || pSeg.matches == nil {
			return false
		}
		if !sameElems(eSeg.matches, pSeg.matches) {
			return false
		}
	}
	return true
}

func sameElems(ss1, ss2 []string) bool {
	seen := make(map[string]bool)
	for _, s1 := range ss1 {
		seen[s1] = true
	}
	for _, s2 := range ss2 {
		if !seen[s2] {
			return false
		}
	}
	return true
}

func pathsMatch(pattern []segment, reqPath []string) (vars Vars, ok bool) {

	vars = make(Vars)

	if len(pattern) != len(reqPath) {
		return nil, false
	}

	for i, seg := range pattern {

		// Wildcard segment.
		if seg.matches == nil {
			if seg.varName != "" {
				vars[seg.varName] = reqPath[i]
			}
			continue
		}

		found := false
		for _, match := range seg.matches {
			if match == reqPath[i] {
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}

		if seg.varName != "" {
			vars[seg.varName] = reqPath[i]
		}
	}

	if len(vars) == 0 {
		vars = nil
	}

	return vars, true
}

func (rt *Router) expandPattern(pattern string) []segment {

	if pattern == "" {
		return nil
	}

	var segments []segment
	subPatterns := explodePath(pattern)
	seenVars := make(map[string]bool)

	for _, sp := range subPatterns {

		var literal *string
		var varName string
		var matches []string
		var listStart int
		var errs []error
		illegal := ":[]"

		switch {

		// Literal segment.
		case sp[0] != ':' && sp[0] != '[':
			if idx := strings.IndexAny(sp, illegal); idx != -1 {
				errs = append(errs, illegalChar(pattern, "literal", illegal))
			}
			literal = &sp

		// Segement with variable.
		case sp[0] == ':':
			listStart = strings.IndexRune(sp, '[')
			if listStart == -1 {
				varName = sp[1:]
				errs = append(errs, validVarName(pattern, varName, seenVars)...)
				seenVars[varName] = true
				break
			}
			varName = sp[1:listStart]
			errs = append(errs, validVarName(pattern, varName, seenVars)...)
			seenVars[varName] = true
			fallthrough

		// Segment containing whitelist.
		case sp[0] == '[':
			if sp[len(sp)-1] != ']' {
				errs = append(errs, fmt.Errorf(
					`pattern segment contains "[" but doesn't end with "]"`+"\n"+
						"pattern: %q", pattern))
			}
			matches = strings.Split(sp[listStart+1:len(sp)-1], ",")
		}

		if idx := strings.IndexAny(varName, illegal); idx != -1 {
			errs = append(errs, illegalChar(pattern, "variable", illegal))
		}
		for i := range matches {
			matches[i] = strings.TrimSpace(matches[i])
			if idx := strings.IndexAny(matches[i], illegal); idx != -1 {
				errs = append(errs, illegalChar(pattern, "whitelist", illegal))
			}
		}

		if literal != nil {
			matches = []string{*literal}
		}

		if len(errs) > 0 {
			rt.Errors = append(rt.Errors, errs...)
			continue
		}

		segments = append(segments, segment{
			raw:     sp,
			varName: varName,
			matches: matches,
		})
	}

	return segments
}

func validVarName(pattern, name string, vars map[string]bool) (errs []error) {
	if name == "" {
		errs = append(errs, fmt.Errorf(`no variable name after ":"\npattern: %q`, pattern))
		return errs
	}
	if _, ok := vars[name]; ok {
		errs = append(errs, fmt.Errorf("duplicate instances of variable name %q\npattern: %q", name, pattern))
		return errs
	}
	return errs
}

func illegalChar(pattern, kind, chars string) error {
	var s string
	cc := strings.Split(chars, "")
	for i, c := range cc {
		if i == len(cc)-1 {
			s += fmt.Sprintf(" or %q", c)
			break
		}
		s += fmt.Sprintf("%q,", c)
	}
	return fmt.Errorf("pattern segment %s cannot contain %s\npattern: %q", kind, s, pattern)
}

func in(ss []string, s string) bool {
	for i := range ss {
		if ss[i] == s {
			return true
		}
	}
	return false
}

func explodePath(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}

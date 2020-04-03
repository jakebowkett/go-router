package router

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

var methods = []string{
	"HEAD",
	"GET",
	"POST",
	"PUT",
	"PATCH",
	"DELETE",
	"CONNECT",
	"OPTIONS",
	"TRACE",
}

type Request = struct {
	Id      string
	Request *http.Request
	Vars    Vars
	User    interface{}
	Status  int
	Error   error
}

type Handler = func(w http.ResponseWriter, r *Request)
type Deferred = func(reqId string, r *http.Request, d time.Time)
type Recover = func(w http.ResponseWriter, reqId string, recovered interface{})
type Middleware = func(r *Request) (status int, err error)

type segment struct {
	raw     string
	varName string
	matches []string
}

type Vars = map[string]string

type stratum struct {
	method     string
	pattern    []segment
	handler    Handler
	middleware []Middleware
	call       Middleware
}

type Router struct {
	strata      []stratum
	Error       Handler
	Errors      []error
	Redirect    func(w http.ResponseWriter, r *Request) bool
	Before      func(w http.ResponseWriter, r *http.Request)
	IdGenerator func() (id string)
	Deferred    Deferred
	Recover     Recover
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	start := time.Now()

	// Request ID is an empty string if there's no generator.
	var reqId string
	if rt.IdGenerator != nil {
		reqId = rt.IdGenerator()
	}

	if rt.Deferred != nil {
		defer rt.Deferred(reqId, r, start)
	}

	/*
		This is after the first deferred call because they're
		executed in reverse order. We need to call w.WriteHeader
		before calling log.End. The call to log.BadRequest in
	*/
	defer func() {
		if recovered := recover(); recovered != nil {
			if rt.Recover == nil {
				return
			}
			rt.Recover(w, reqId, recovered)
		}
	}()

	request := &Request{
		Id:      reqId,
		Request: r,
		Vars:    make(Vars),
	}

	if rt.Redirect != nil && rt.Redirect(w, request) {
		return
	}
	if rt.Before != nil {
		rt.Before(w, r)
	}

	var statusCode int
	var seenRoute bool

	reqPath := explodePath(r.URL.Path)

	for _, s := range rt.strata {

		if s.call != nil {
			if code, err := s.call(request); err != nil {
				request.Error = err
				statusCode = code
				seenRoute = true
				break
			}
			continue
		}

		if s.method != r.Method {
			continue
		}

		vars, ok := pathsMatch(s.pattern, reqPath)
		if !ok {
			continue
		}
		seenRoute = true

		for _, mw := range s.middleware {
			if code, err := mw(request); err != nil {
				request.Error = err
				statusCode = code
				break
			}
		}

		request.Vars = vars
		s.handler(w, request)

		return
	}

	if !seenRoute {
		statusCode = 404
	}

	if rt.Error != nil {
		request.Status = statusCode
		rt.Error(w, request)
		return
	}

	// if statusCode >= 300 && statusCode < 400 {
	// 	return
	// }
	w.WriteHeader(statusCode)
}

func (rt *Router) Use(fn Middleware) {
	if fn == nil {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			"route %d: function supplied to Use is nil",
			len(rt.strata),
		))
	}
	rt.strata = append(rt.strata, stratum{call: fn})
}

func (rt *Router) Get(pattern string, handler Handler, middleware ...Middleware) {
	rt.Add("GET", pattern, handler, middleware...)
}
func (rt *Router) Pst(pattern string, handler Handler, middleware ...Middleware) {
	rt.Add("POST", pattern, handler, middleware...)
}
func (rt *Router) Put(pattern string, handler Handler, middleware ...Middleware) {
	rt.Add("PUT", pattern, handler, middleware...)
}
func (rt *Router) Pat(pattern string, handler Handler, middleware ...Middleware) {
	rt.Add("PATCH", pattern, handler, middleware...)
}
func (rt *Router) Del(pattern string, handler Handler, middleware ...Middleware) {
	rt.Add("DELETE", pattern, handler, middleware...)
}
func (rt *Router) Add(method, pattern string, handler Handler, middleware ...Middleware) {

	stratIdx := len(rt.strata)

	if !in(methods, method) {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			`route %d: invalid HTTP method "%s" for pattern %s`,
			stratIdx,
			method,
			pattern,
		))
	}

	if handler == nil {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			"route %d: no handler function supplied for %s %s",
			stratIdx,
			method,
			pattern,
		))
	}

	for i, mw := range middleware {
		if mw == nil {
			rt.Errors = append(rt.Errors, fmt.Errorf(
				"route %d: middleware at index %d for %s %s is nil",
				stratIdx,
				i,
				method,
				pattern,
			))
		}
	}

	if !rt.isUnique(method, pattern) {
		rt.Errors = append(rt.Errors, fmt.Errorf(
			"route %d: unreachable route due to duplicate method and pattern pairing: %s %s",
			stratIdx,
			method,
			pattern,
		))
	}

	rt.strata = append(rt.strata, stratum{
		method:     method,
		pattern:    rt.expandPattern(pattern),
		handler:    handler,
		middleware: middleware,
	})
}

func (rt *Router) isUnique(method, pattern string) bool {
	unique := true
	for _, s := range rt.strata {
		_, ok := pathsMatch(s.pattern, explodePath(pattern))
		if s.method != method || !ok {
			continue
		}
		unique = false
	}
	return unique
}

func (rt *Router) expandPattern(pattern string) []segment {

	var segments []segment
	subPatterns := explodePath(pattern)

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
				break
			}
			varName = sp[1:listStart]
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

func pathsMatch(pattern []segment, reqPath []string) (vars Vars, ok bool) {

	vars = make(Vars)

	if len(pattern) != len(reqPath) {
		return nil, false
	}

	for i, seg := range pattern {

		// wildcard segment
		if seg.matches == nil {
			if seg.varName != "" {
				vars[seg.varName] = reqPath[i]
			}
			continue
		}

		found := false
		for _, match := range seg.matches {
			/*
				If the current path segment matches
				a segment pattern beginning with a
				negation symbol (^) the paths are
				considered not to match.
			*/
			if strings.HasPrefix(match, "^") {
				if match[1:] == reqPath[i] {
					return nil, false
				}
			} else {
				if match == reqPath[i] {
					found = true
					break
				}
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

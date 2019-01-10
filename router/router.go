package router

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Handler = func(reqId string, w http.ResponseWriter, r *http.Request, vars Vars)
type Deferred = func(reqId string, duration int, r *http.Request)

type segment struct {
	raw     string
	varName string
	matches []string
}

type Vars = map[string]string

type Router struct {
	methods       []string
	patterns      [][]segment
	handlers      []Handler
	NotFound      Handler
	Errors        []error
	BeforeHandler func(reqId string)
	IdGenerator   func() (id string)
	Deferred      Deferred
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	start := time.Now()

	// Request ID is an empty string if there's no generator.
	var reqId string
	if rt.IdGenerator != nil {
		reqId = rt.IdGenerator()
	}

	if rt.BeforeHandler != nil {
		rt.BeforeHandler(reqId)
	}

	if rt.Deferred != nil {
		defer rt.Deferred(reqId, int(time.Since(start).Nanoseconds()), r)
	}

	reqPath := explodePath(r.URL.Path)

	for i := range rt.patterns {
		if rt.methods[i] != r.Method {
			continue
		}
		vars, ok := pathsMatch(rt.patterns[i], reqPath)
		if !ok {
			continue
		}
		rt.handlers[i](reqId, w, r, vars)
		return
	}

	if rt.NotFound != nil {
		rt.NotFound(reqId, w, r, make(Vars))
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (rt *Router) Get(pattern string, handler Handler) {
	rt.add("GET", pattern, handler)
}

func (rt *Router) Post(pattern string, handler Handler) {
	rt.add("POST", pattern, handler)
}

func (rt *Router) add(method, pattern string, handler Handler) {
	rt.methods = append(rt.methods, method)
	rt.patterns = append(rt.patterns, rt.expandPattern(pattern))
	rt.handlers = append(rt.handlers, handler)
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

		if !in(seg.matches, reqPath[i]) {
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

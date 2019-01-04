package router

import (
	"net/http"
	"strings"
	"time"
)

type Handler func(reqId string, w http.ResponseWriter, r *http.Request)
type Deferred func(reqId string, status, duration int, r *http.Request)

type resWriterWrapper struct {
	status int
	rw     http.ResponseWriter
}

func (rw *resWriterWrapper) Header() http.Header {
	return rw.rw.Header()
}

func (rw *resWriterWrapper) Write(b []byte) (int, error) {
	return rw.rw.Write(b)
}

func (rw *resWriterWrapper) WriteHeader(code int) {
	rw.status = code
	rw.rw.WriteHeader(code)
}

type Router struct {
	methods     []string
	routes      [][]string
	handlers    []Handler
	NotFound    Handler
	IdGenerator func() (id string)
	Deferred    func(reqId string, status, duration int, r *http.Request)
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	start := time.Now()
	w = &resWriterWrapper{rw: w}

	// Request ID is an empty string if there's no generator.
	var reqId string
	if rt.IdGenerator != nil {
		reqId = rt.IdGenerator()
	}

	if rt.Deferred != nil {
		defer func() {
			status := -1
			wrapper, ok := w.(*resWriterWrapper)
			if ok {
				status = wrapper.status
				if status == 0 {
					status = 200
				}
			}
			duration := int(time.Since(start).Nanoseconds())
			rt.Deferred(reqId, status, duration, r)
		}()
	}

	reqPath := explodePath(r.URL.Path)

	for i := range rt.routes {
		if rt.methods[i] != r.Method {
			continue
		}
		if !pathsMatch(rt.routes[i], reqPath) {
			continue
		}
		rt.handlers[i](reqId, w, r)
		return
	}

	if rt.NotFound != nil {
		rt.NotFound(reqId, w, r)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (rt *Router) Get(route string, handler func(string, http.ResponseWriter, *http.Request)) {
	rt.add("GET", route, handler)
}

func (rt *Router) Post(route string, handler func(string, http.ResponseWriter, *http.Request)) {
	rt.add("POST", route, handler)
}

func (rt *Router) add(method, route string, handler Handler) {
	rt.methods = append(rt.methods, method)
	rt.routes = append(rt.routes, explodePath(route))
	rt.handlers = append(rt.handlers, handler)
}

func pathsMatch(routerPath, requestPath []string) bool {

	if len(routerPath) != len(requestPath) {
		return false
	}

	for i := range routerPath {

		rp := routerPath[i]

		if rp == "*" {
			continue
		}

		if strings.HasPrefix(rp, "[") && strings.HasSuffix(rp, "]") {
			rp := rp[1 : len(rp)-1]
			if in(strings.Split(rp, "|"), requestPath[i]) {
				continue
			}
		}

		if rp != requestPath[i] {
			return false
		}
	}

	return true
}

func in(ss []string, s string) bool {
	for i := range ss {
		if ss[i] == s {
			return true
		}
	}
	return false
}

func explodePath(route string) []string {
	return strings.Split(strings.Trim(route, "/"), "/")
}

// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package server

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/vanguard"

	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dataapi/v1/dataapiv1connect"
)

// restPrefix is where the REST transcoding is served.
const restPrefix = "/v1/data_api/"

// maxReadBytes bounds request bodies; the Data API's requests are small.
const maxReadBytes = 1 << 20

// Handler returns the HTTP handler of svc: the Connect, gRPC and gRPC-Web
// protocols at the procedures' paths, the REST transcoding under
// /v1/data_api/, and /healthz. JSON uses the proto field names. The
// interceptors run in order, the first outermost.
func Handler(svc dataapiv1connect.DataApiServiceHandler, interceptors ...connect.ServerInterceptor) (http.Handler, error) {
	server := connect.NewServer(interceptors...)
	dataapiv1connect.RegisterDataApiServiceHandler(server, svc)

	mux := http.NewServeMux()
	json := connectproto.NewJSONCodec()
	json.MarshalOptions.UseProtoNames = true
	connecthttp.Mount(mux, server,
		connecthttp.WithCodecs(connectproto.NewBinaryCodec(), json),
		connecthttp.WithReadMaxBytes(maxReadBytes),
	)
	rest := http.NewServeMux()
	if err := vanguard.Mount(rest, server,
		vanguard.WithCodec(func(res connectproto.TypeResolver) vanguard.RESTCodec {
			c := vanguard.NewJSONCodec(res)
			c.MarshalOptions.UseProtoNames = true
			return c
		}),
		vanguard.WithMaxReadBytes(maxReadBytes),
	); err != nil {
		return nil, fmt.Errorf("server: rest: %w", err)
	}
	mux.Handle(restPrefix, DateQueries(rest))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}

// isoDate is a calendar date in ISO 8601 extended form.
var isoDate = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// DateQueries lets REST queries give a google.type.Date as one ISO 8601
// date: a parameter whose last segment ends in "_date" and whose value is
// YYYY-MM-DD (filter.from_date=2026-01-31) is rewritten as the date's
// year, month and day fields, which the transcoding decodes.
func DateQueries(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		changed := false
		for key, values := range q {
			last := key[strings.LastIndexByte(key, '.')+1:]
			if !strings.HasSuffix(last, "_date") || len(values) != 1 {
				continue
			}
			m := isoDate.FindStringSubmatch(values[0])
			if m == nil {
				continue
			}
			delete(q, key)
			for i, field := range []string{"year", "month", "day"} {
				n, _ := strconv.Atoi(m[i+1])
				q.Set(key+"."+field, strconv.Itoa(n))
			}
			changed = true
		}
		if changed {
			r = r.Clone(r.Context())
			r.URL.RawQuery = q.Encode()
		}
		next.ServeHTTP(w, r)
	})
}

// Package rpctest serves the API in tests: the handler stack of the
// server (interceptors, services, Connect and REST routes) over a fresh
// PostgreSQL database per test, with an organization, a full-access API
// key and an admin user session seeded. One PostgreSQL server serves every
// test of a package; the test binary must run [Main].
//
//	func TestMain(m *testing.M) { os.Exit(rpctest.Main(m)) }
//
//	func TestCreate(t *testing.T) {
//		s := rpctest.New(t)
//		c := catalogv1connect.NewBillableMetricServiceClient(s.Connect(s.APIKey))
//		resp, err := c.CreateBillableMetric(t.Context(), req)
//		...
//		status, body := s.REST(t, s.APIKey, "GET", "/v1/billable_metrics", nil)
//	}
//
// Databases are cloned from a template migrated with the store's
// migrations or, with TANVO_STORE_TEST_SCHEMA=source, loaded from
// libs/store/schema (a schema change without its migration yet). Tests
// are skipped when no PostgreSQL server is reachable.
package rpctest
